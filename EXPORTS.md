# Browser exports and `serve`

`postfix-report serve` is an optional foreground HTTP service. It serves the
existing HTML dashboards and generates a complete PDF or editable ODT document
when a browser requests it. Each single-period export covers **one day, ISO week, month or
year**, with every report section, charts, data tables, coverage warnings and
the same metric definitions and anonymization as HTML. Fonts and chart images
are embedded; the downloaded file works without the original report directory.

The existing `update` command and timer continue importing logs and generating
HTML. The daemon reads an existing database and never runs migrations, imports
logs or invokes a browser/Office converter. Its documents use the last completed
import; failed/incomplete imports must be repaired with `update` first.
Archive exports are generated on demand; `update` does not pre-generate PDF/ODT files.

## Complete archive in one PDF or ODT

With `serve`, the top of the main index includes **Export complete archive**
(**Esporta archivio completo** in Italian). It exports a single document containing
all selected reports, with a cover, page numbers and a linked table of contents.
PDF also includes bookmarks; ODT has an editable native table of contents.

The default selects the entire observed archive and every enabled report type.
Uncheck **Entire archive** to choose inclusive local dates, or select fewer report
types. **Check selection** previews the effective date range and report counts;
PDF/ODT buttons become available after a successful preview. Changing any input
requires checking it again. Preview is also run when the panel first loads.

Reports are grouped yearly, monthly, weekly, then daily; each group is in
chronological order. The index lists groups and individual periods. **Selections
include whole calendar periods overlapping the chosen dates**, so a selected day
can include its whole month/year. They do not clip a monthly report to that day.
Counts across overlapping periods must not be added together. Missing coverage
and partially observed periods retain their existing warnings and semantics.

One export is limited to **200 reports**, **2000 pages**, and **32 MiB** of content
and output; archive preparation also has a 32 MiB presentation-model budget.
The preview reports when the report-count limit is exceeded. Page/byte limits
are checked during generation. Oversized documents fail explicitly; nothing is
silently omitted. Reduce the range or selected types to fit these limits.

All selected reports are queried from one completed WAL snapshot, which is
released before image generation and pagination. Updates can continue while
queries run. A preview is informational: the export recomputes its selection in
its own snapshot if new data has arrived since the preview. Job timeouts, queues,
ownership, privacy invalidation and private downloads are shared with single
period exports. No database migration or new systemd/proxy settings are needed.

Both formats use a single-line running header: server/site name on the left,
period in the center and UTC generation date aligned right. The archive header
uses its title and selected interval. Header text is fitted without truncation;
values that cannot fit at the minimum font size cause an explicit error. Full
titles are also retained on the cover and in the document body.

```sh
postfix-report -config config.yaml export --all --format pdf --output ./archive.pdf
postfix-report -config config.yaml export --all --format odt --output ./archive.odt
postfix-report -config config.yaml export --all --from 2026-10-01 --to 2026-10-31 \
  --kinds year,month,week,day --format pdf --output ./october.pdf
```

`--all` cannot be combined with a single-period flag. `--from` and `--to` must
appear together; dates use `YYYY-MM-DD` in `input.timezone`. `--kinds` accepts a
comma-separated subset of `year,month,week,day`, requires `--all`, and defaults to
all enabled types. Empty, duplicate, unknown or disabled types are rejected.
Whole gaps within the archive remain represented as missing observations.

## Local use

Prepare the database, anonymization key and HTML with the normal update command,
then start the service under the same account with access to the private DB:

```sh
postfix-report -config config.yaml update
postfix-report -config config.yaml serve
```

Open `http://127.0.0.1:8080/`, select a period, and press **PDF** or **ODT** at the
bottom of the report. The page displays progress and then a download link.
`--listen`, `--prefix` and `--origin` override the corresponding web settings.
Only numeric loopback listeners are accepted; other clients connect through
the authenticated reverse proxy below. Direct loopback access trusts local OS
users and does not provide application login authentication.

The standard generated HTML remains usable with a static web server or directly
from disk. Export controls are added by `serve` when it serves a period page;
they are absent from the static-only version.

## HTTPS and systemd

Example configuration for a public origin and URL prefix:

```yaml
web:
  listen: 127.0.0.1:8080
  prefix: /postfix-report/
  public_origin: https://reports.example.org
  workers: 1
  queue_size: 4
  job_timeout: 2m
  job_ttl: 10m
```

Replace the origin with the actual HTTPS origin, including its external port
when nonstandard. It must not contain a path or a trailing slash. The prefix
starts and ends with `/`. Preserve the external Host header and the path prefix
in the reverse proxy. Do not strip `/postfix-report/` before forwarding it.

Use `deploy/nginx/postfix-report-web.conf` or
`deploy/apache/postfix-report-web.conf` **instead of** the static Alias profile.
HTTPS and authentication must cover the whole prefix, including job creation,
status, script and download routes. Forward the Basic Authorization header so
job ownership also distinguishes users when credentials change in a browser.
The backend accepts no forwarded-identity headers and exposes no import or
administration endpoints. An authenticated proxy is required for remote use.

The daemon sends CSP allowing same-origin API calls. Do not append the static
profile's `connect-src 'none'`. Browser requests use an HttpOnly, SameSite=Strict
session cookie plus an Origin check and CSRF token. Job identifiers alone grant
no access to a result; both the browser session and Basic-auth user must match.

Packages ship a separate `postfix-report-web.service`, disabled by default. After
preparing data and configuring the proxy, an administrator can enable it:

```sh
sudo systemctl enable --now postfix-report-web.service
sudo systemctl status postfix-report-web.service
sudo journalctl -u postfix-report-web.service
```

The update timer remains independent. The web unit runs without root or access
to Postfix log groups, with a loopback listener and resource limits. SQLite may
need writable WAL/SHM sidecars in `/var/lib/postfix-report`; SQL connections are
opened read-only with `query_only` enabled. Database/output paths changed from
the packaging defaults require matching systemd path overrides. SIGTERM closes
HTTP, cancels jobs and waits for their resources to be released.

## One-shot CLI export

```sh
postfix-report -config config.yaml export --date 2026-10-25 --format pdf --output ./day.pdf
postfix-report -config config.yaml export --week 2026-W43 --format odt --output ./week.odt
postfix-report -config config.yaml export --month 2026-10 --format pdf --output ./month.pdf
postfix-report -config config.yaml export --year 2026 --format odt --output ./year.odt
```

Exactly one period or `--all` is required. Disabled report kinds and periods outside the
observed data range are rejected. An existing file or symlink is never replaced.
New CLI files are private (`0600`) and published only after writing completes.
Choose the destination explicitly; downloaded copies are independent documents.

## Consistency, privacy and limits

- Each export acquires a short shared process lock and pins a read transaction
  from a completed writer run. It then releases the process lock so `update` can
  continue while report queries read the same WAL snapshot. The transaction is
  closed before charts or document pagination are rendered. Preparation has a
  30-second limit; no database snapshots or backup files are created.
- Every job and download reloads the configuration. Changes invalidate results;
  privacy changes also block old HTML until `update` has reconciled the archive.
  Changes to web settings or database/output directories require restarting the
  daemon. Invalid/unreadable configuration fails closed. A restarted daemon
  discards all previous sessions and jobs.
- Results are kept only in private process memory, not as public files. There
  is no cross-job cache. Defaults: one worker, four queued requests, two minutes
  per running job, ten-minute result lifetime. Completed results together are
  capped at 64 MiB; at most 32 job records and 32 MiB per document are retained.
  Queue/memory saturation returns a retryable error rather than growing without
  bounds. Queued jobs also expire. HTTP responses are `private, no-store`.
- The renderer caps content/output at 32 MiB and pagination at 2000 pages.
  Exact delivery-delay percentiles allow up to one million samples per query
  in document exports; larger requests fail explicitly, never approximate or
  omit the metric. The ordinary HTML/stats behavior is unchanged.
- PDF text uses embedded Liberation Sans; unsupported glyphs cause an explicit
  generation failure. ODT preserves Unicode text. Inspect the service log when
  an export fails; use ODT for identifiers outside the PDF font repertoire.
- ODT contains editable text/tables and embedded PNG charts. Opening or editing
  it in a different office suite may change pagination. No PDF/A, PDF/UA or
  office-suite conformance certification is claimed.

The renderer adapts the owner's local adscan document engine. Dependencies are
fixed in `go.mod`; third-party license texts are in `docs/licenses/`. No
LibreOffice, Chromium or external conversion service is needed at runtime.
