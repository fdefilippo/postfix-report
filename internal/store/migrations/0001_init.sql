-- 0001_init: base schema for postfix-report.
-- Raw layer: sources, imports, events. Derived layer: instances, recipients,
-- deliveries, rejects, security_events. Aggregate layer: agg_hour, agg_dim.
-- Report layer: dirty_pages, report_pages.

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- A log source is identified by the content of its first bytes
-- (decompressed), not by its path: rotation renames files.
CREATE TABLE sources (
    id          INTEGER PRIMARY KEY,
    fingerprint TEXT    NOT NULL,           -- hex sha256 of the first fp_len bytes
    fp_len      INTEGER NOT NULL,
    path        TEXT    NOT NULL,           -- last path the source was seen at
    compressed  INTEGER NOT NULL DEFAULT 0,
    file_size   INTEGER NOT NULL DEFAULT 0, -- on-disk size at last import
    file_mtime  INTEGER NOT NULL DEFAULT 0,
    offset      INTEGER NOT NULL DEFAULT 0, -- decompressed bytes consumed (whole lines)
    lines       INTEGER NOT NULL DEFAULT 0,
    first_ts    INTEGER,
    last_ts     INTEGER,
    clock_state TEXT,                       -- JSON logparse.ClockState
    tail_state  TEXT,                       -- JSON duplicate counters of the last second
    complete    INTEGER NOT NULL DEFAULT 0, -- 1 = compressed file fully read
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (fingerprint, fp_len)
);

CREATE TABLE imports (
    id          INTEGER PRIMARY KEY,
    command     TEXT    NOT NULL,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    status      TEXT    NOT NULL,           -- running / ok / failed
    files       INTEGER NOT NULL DEFAULT 0,
    lines       INTEGER NOT NULL DEFAULT 0,
    events      INTEGER NOT NULL DEFAULT 0,
    duplicates  INTEGER NOT NULL DEFAULT 0,
    error       TEXT
);

CREATE TABLE import_files (
    id           INTEGER PRIMARY KEY,
    import_id    INTEGER NOT NULL REFERENCES imports(id),
    source_id    INTEGER NOT NULL REFERENCES sources(id),
    path         TEXT    NOT NULL,
    start_offset INTEGER NOT NULL,
    end_offset   INTEGER NOT NULL,
    lines        INTEGER NOT NULL,
    events       INTEGER NOT NULL,
    duplicates   INTEGER NOT NULL,
    non_postfix  INTEGER NOT NULL,
    malformed    INTEGER NOT NULL,
    unknown      INTEGER NOT NULL,
    first_ts     INTEGER,
    last_ts      INTEGER
);

-- Lines that were recognized as Postfix but not classified, per process.
CREATE TABLE unknown_lines (
    source_id INTEGER NOT NULL REFERENCES sources(id),
    proc      TEXT    NOT NULL,
    count     INTEGER NOT NULL,
    sample    TEXT,                          -- first example (kept in DB only)
    PRIMARY KEY (source_id, proc)
);

-- Observed time intervals (coverage), split at gaps > coverage.gap_threshold.
CREATE TABLE segments (
    id        INTEGER PRIMARY KEY,
    source_id INTEGER NOT NULL REFERENCES sources(id),
    host      TEXT    NOT NULL,
    start_ts  INTEGER NOT NULL,
    end_ts    INTEGER NOT NULL
);
CREATE INDEX segments_range ON segments (start_ts, end_ts);

CREATE TABLE events (
    id          INTEGER PRIMARY KEY,
    source_id   INTEGER NOT NULL REFERENCES sources(id),
    line_offset INTEGER NOT NULL,
    ukey        INTEGER NOT NULL,           -- duplicate key (hash of line + occurrence)
    ts          INTEGER NOT NULL,
    host        TEXT    NOT NULL,
    inst        TEXT    NOT NULL,
    proc        TEXT    NOT NULL,
    pid         INTEGER,
    qid         TEXT,
    kind        TEXT    NOT NULL,
    sender      TEXT,
    rcpt        TEXT,
    orig        TEXT,
    client      TEXT,
    ip          TEXT,
    relay       TEXT,
    size        INTEGER,
    nrcpt       INTEGER,
    delay       REAL,
    delays      TEXT,
    dsn         TEXT,
    status      TEXT,
    code        INTEGER,
    stage       TEXT,
    text        TEXT,
    method      TEXT,
    user        TEXT,
    uid         INTEGER,
    new_qid     TEXT,
    tls         INTEGER,
    auth_ok     INTEGER,
    auth_ko     INTEGER
);
CREATE UNIQUE INDEX events_ukey ON events (ts, ukey);
CREATE INDEX events_host_ts ON events (host, ts, id);
CREATE INDEX events_ts_kind ON events (ts, kind);

-- Correlation watermark per host: events after (wm_ts, wm_id) are new.
CREATE TABLE corr_state (
    host  TEXT PRIMARY KEY,
    wm_ts INTEGER NOT NULL,
    wm_id INTEGER NOT NULL
);

-- One row per queue-instance lifecycle (a Queue ID may be reused).
CREATE TABLE instances (
    id              INTEGER PRIMARY KEY,
    host            TEXT    NOT NULL,
    inst            TEXT    NOT NULL,
    qid             TEXT    NOT NULL,
    origin          TEXT    NOT NULL,   -- smtp/local/reinject/notification/probe/internal/unknown
    state           TEXT    NOT NULL,   -- open/removed/rejected/discarded/superseded/stale
    first_ts        INTEGER NOT NULL,
    last_ts         INTEGER NOT NULL,
    accepted_ts     INTEGER,            -- first "queue active"
    accept_observed INTEGER NOT NULL DEFAULT 0,
    removed_ts      INTEGER,
    sender          TEXT,
    size            INTEGER,
    nrcpt           INTEGER,
    message_id      TEXT,
    has_message_id  INTEGER NOT NULL DEFAULT 0,
    client          TEXT,
    ip              TEXT,
    sasl_method     TEXT,
    sasl_user       TEXT,
    uid             INTEGER,
    parent_id       INTEGER REFERENCES instances(id), -- reinjection -> original
    root_id         INTEGER REFERENCES instances(id), -- logical message root
    notif_type      TEXT,
    notif_parent_id INTEGER REFERENCES instances(id),
    filtered        INTEGER NOT NULL DEFAULT 0,       -- handed to the content filter
    expired_ts      INTEGER,
    flags           TEXT NOT NULL DEFAULT ''          -- anomaly flags, comma separated
);
CREATE INDEX instances_qid ON instances (host, inst, qid, state);
CREATE INDEX instances_accepted ON instances (accepted_ts);
CREATE INDEX instances_parent ON instances (parent_id);
CREATE INDEX instances_root ON instances (root_id);

CREATE TABLE recipients (
    id          INTEGER PRIMARY KEY,
    instance_id INTEGER NOT NULL REFERENCES instances(id),
    rcpt        TEXT    NOT NULL,
    orig        TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL,   -- pending/delivered/failed/expired/filtered/probe/discarded
    first_ts    INTEGER,
    final_ts    INTEGER,
    attempts    INTEGER NOT NULL DEFAULT 0,
    deferrals   INTEGER NOT NULL DEFAULT 0,
    final_delay REAL,
    final_dsn   TEXT,
    final_agent TEXT,
    UNIQUE (instance_id, rcpt, orig)
);
CREATE INDEX recipients_final ON recipients (final_ts);

CREATE TABLE deliveries (
    id           INTEGER PRIMARY KEY,
    event_id     INTEGER NOT NULL UNIQUE REFERENCES events(id),
    instance_id  INTEGER NOT NULL REFERENCES instances(id),
    recipient_id INTEGER NOT NULL REFERENCES recipients(id),
    ts           INTEGER NOT NULL,
    agent        TEXT    NOT NULL,
    rcpt         TEXT    NOT NULL,
    relay        TEXT,
    delay        REAL,
    dsn          TEXT,
    status       TEXT    NOT NULL,
    text         TEXT,
    class        TEXT    NOT NULL,   -- delivery / filter / probe
    after_defer  INTEGER NOT NULL DEFAULT 0  -- sent/bounced after >= 1 deferral of the same recipient
);
CREATE INDEX deliveries_ts ON deliveries (ts, class, status);
CREATE INDEX deliveries_instance ON deliveries (instance_id);

CREATE TABLE rejects (
    id          INTEGER PRIMARY KEY,
    event_id    INTEGER NOT NULL UNIQUE REFERENCES events(id),
    instance_id INTEGER REFERENCES instances(id),
    ts          INTEGER NOT NULL,
    host        TEXT    NOT NULL,
    action      TEXT    NOT NULL,   -- reject / milter-reject / discard / hold ...
    source      TEXT    NOT NULL,   -- smtpd / cleanup
    stage       TEXT,
    code        INTEGER,
    dsn         TEXT,
    reason      TEXT,
    client      TEXT,
    ip          TEXT,
    sender      TEXT,
    rcpt        TEXT
);
CREATE INDEX rejects_ts ON rejects (ts);

CREATE TABLE security_events (
    id       INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL UNIQUE REFERENCES events(id),
    ts       INTEGER NOT NULL,
    host     TEXT    NOT NULL,
    kind     TEXT    NOT NULL,      -- sasl_fail / tls_error / session_abort
    ip       TEXT,
    client   TEXT,
    method   TEXT,
    detail   TEXT
);
CREATE INDEX security_ts ON security_events (ts);

-- Hourly aggregates in local time (long format: one row per metric).
CREATE TABLE agg_hour (
    day    TEXT    NOT NULL,   -- YYYY-MM-DD local
    hour   INTEGER NOT NULL,   -- 0..23 local
    metric TEXT    NOT NULL,
    value  REAL    NOT NULL,
    PRIMARY KEY (day, hour, metric)
) WITHOUT ROWID;

-- Daily dimension counters used for rankings.
CREATE TABLE agg_dim (
    day   TEXT    NOT NULL,
    dim   TEXT    NOT NULL,
    key   TEXT    NOT NULL,
    value INTEGER NOT NULL,
    PRIMARY KEY (day, dim, key)
) WITHOUT ROWID;

-- Days whose aggregates must be recomputed.
CREATE TABLE dirty_days (
    day TEXT PRIMARY KEY
) WITHOUT ROWID;

-- Report pages to regenerate (kind: day/week/month/year/index).
CREATE TABLE dirty_pages (
    kind TEXT NOT NULL,
    key  TEXT NOT NULL,
    PRIMARY KEY (kind, key)
) WITHOUT ROWID;

CREATE TABLE report_pages (
    path         TEXT PRIMARY KEY,
    kind         TEXT    NOT NULL,
    key          TEXT    NOT NULL,
    hash         TEXT    NOT NULL,
    generated_at INTEGER NOT NULL
);
