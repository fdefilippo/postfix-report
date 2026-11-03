-- Rebuild sources without the unique short-head constraint. The migration
-- runner disables FK enforcement outside the transaction and checks all FK
-- references before commit. Existing IDs and every referring row are retained.
CREATE TABLE sources_new (
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
    checkpoint_hash TEXT NOT NULL DEFAULT ''
);

INSERT INTO sources_new(id,fingerprint,fp_len,path,compressed,file_size,file_mtime,offset,lines,first_ts,last_ts,clock_state,tail_state,complete,created_at,updated_at) SELECT id,fingerprint,fp_len,path,compressed,file_size,file_mtime,offset,lines,first_ts,last_ts,clock_state,tail_state,complete,created_at,updated_at FROM sources;
DROP TABLE sources;
ALTER TABLE sources_new RENAME TO sources;
CREATE INDEX sources_head ON sources(fp_len,fingerprint);

-- Pending announcements are committed with the correlation watermark.
CREATE TABLE pending_notifications (
    host TEXT NOT NULL,
    inst TEXT NOT NULL,
    qid TEXT NOT NULL,
    type TEXT NOT NULL,
    parent_id INTEGER NOT NULL REFERENCES instances(id),
    ts INTEGER NOT NULL,
    PRIMARY KEY(host,inst,qid)
) WITHOUT ROWID;
-- Old watermarks did not retain pending announcements: rebuild derived state
-- once, leaving raw data intact, so the upgrade also recovers lost links.
INSERT INTO meta(key,value) VALUES('correlation_pending_upgrade','1');
