# Browser exports and `serve`

`postfix-report serve` is an optional foreground HTTP service. It serves the
existing HTML dashboards and generates a complete PDF or editable ODT document
when a browser requests it. Each document covers **one day, ISO week, month or
year**, with every report section, charts, data tables, coverage warnings and
the same metric definitions and anonymization as HTML. Fonts and chart images
are embedded; the downloaded file works without the original report directory.

The existing `update` command and timer continue importing logs and generating
HTML. The daemon reads an existing database and never runs migrations, imports
logs or invokes a browser/Office converter. Its documents use the last completed
import; failed/incomplete imports must be repaired with `update` first.
There is no automatic PDF/ODT generation for the entire archive in this mode.

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

Exactly one period is required. Disabled report kinds and periods outside the
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
