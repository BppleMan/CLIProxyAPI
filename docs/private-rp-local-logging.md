# Private RP local logging

Private request logging requires both `CPA_PRIVATE_RP=1` and the explicit
`CPA_PRIVATE_RP_REQUEST_LOG=1` opt-in, together with `request-log: true`.
`logging-to-file: true` enables the application log. Request transcripts include
front-end and upstream messages, responses, system instructions, and supported
media. They must be stored on a private, access-controlled volume. Text written
by the user is content and can itself contain sensitive information.

Credential-like headers, cookies, URL parameters and recognized structured
credential fields are redacted before writing. Malformed structured frames are
omitted. No automatic redactor can recognize every possible secret embedded in
arbitrary user text or binary media. Generic fields named `key` are also
redacted to avoid exposing a credential under an ambiguous name.

`logs-max-total-size-mb` is a total-size cleanup target, not a retention period.
The cleaner runs periodically and removes the oldest completed logs; current
application and active request files are protected. Active files and temporary
usage can temporarily exceed the target. Choose a separate volume with free
space above the target and monitor its actual capacity. There is no fixed
number of days of retention.

Disk writes run in bounded background workers, so a stalled log filesystem does
not block inference. At most 16 requests are captured concurrently, and the
shared upstream capture queue is limited to 96 MiB. Streaming frames larger
than 32 MiB or frames that cannot be safely parsed are omitted. If the writer
cannot keep up, log records can be incomplete; inference continues unchanged.
Capture failures and dropped chunks appear in the management panel and are
process-local counters that reset on restart. In-progress or queued records can
be lost if the process stops. Completed files persist on the configured volume.

The management panel lists completed request logs, supports bounded previews,
time and request-ID filters, media access and clearing completed logs. It also
reports the cleanup target, available disk space when measurable, and capture
failures. Clearing logs leaves active requests intact.
