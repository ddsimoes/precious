# file-viewer Specification

## Purpose
Opens indexed files in the interface without extracting anything and without letting disk content run as part of the application. R7 extends it with optional conversion tools.

## Requirements

### Requirement: Content is served with a type chosen by Precious
`GET /api/entries/{id}/content` SHALL stream the file with a Content-Type from Precious's own extension table, never inferred from the content: raster images (JPEG, PNG, GIF, WebP, AVIF, BMP), SVG as `image/svg+xml`, video (MP4, M4V, WebM, MOV), audio (MP3, M4A, AAC, Ogg, Opus, WAV, FLAC), and PDF as `application/pdf`. Every other file SHALL be sent as `application/octet-stream` with `Content-Disposition: attachment`.

#### Scenario: Type comes from the extension
- **WHEN** the content of `foto.JPG` is requested
- **THEN** the response has `Content-Type: image/jpeg`

#### Scenario: Content never changes the type
- **WHEN** a file named `foto.jpg` actually contains HTML and its content is requested
- **THEN** the response type is still `image/jpeg`

#### Scenario: Other types download
- **WHEN** the content of `Setup.exe`, `pagina.html`, or `dados.xml` is requested
- **THEN** the response is `application/octet-stream` with `Content-Disposition: attachment`

### Requirement: Media supports seeking
Content responses SHALL honor HTTP range requests, so video and audio can seek without downloading the whole file.

#### Scenario: Range request
- **WHEN** the content of an MP4 file is requested with `Range: bytes=1000-1999`
- **THEN** the response is 206 with `Content-Range` and exactly those 1,000 bytes

### Requirement: Text is decoded by Precious
`GET /api/entries/{id}/text` SHALL return `{"encoding","text","truncated","language","markdown"}` for the file's first 1 MiB, decoded by this precedence: a byte-order mark (UTF-8 or UTF-16); otherwise valid UTF-8; otherwise Windows-1252. `truncated` SHALL be true when the file is larger than 1 MiB; `language` SHALL be a syntax hint from the extension or null; `markdown` SHALL be true for Markdown files.

#### Scenario: Windows-1252 text
- **WHEN** the text of a file holding `Meu orçamento` encoded in Windows-1252 is requested
- **THEN** `encoding` names Windows-1252 and `text` is `Meu orçamento`

#### Scenario: UTF-16 with a byte-order mark
- **WHEN** the text of a UTF-16LE file with a byte-order mark is requested
- **THEN** `encoding` names UTF-16 and `text` is the decoded content

#### Scenario: Large file is truncated
- **WHEN** the text of a 5 MiB log file is requested
- **THEN** `text` holds its first 1 MiB decoded and `truncated` is true

#### Scenario: Source and Markdown hints
- **WHEN** the text of `main.go` and of `README.md` is requested
- **THEN** `main.go` has a non-null `language` and `markdown` false, and `README.md` has `markdown` true

### Requirement: Markdown is rendered sanitized
The interface SHALL render Markdown sanitized: no script, event handler, or embedded frame from the file runs, no remote image or other remote content is loaded, and links never navigate the application's page.

#### Scenario: Hostile Markdown
- **WHEN** the owner opens a Markdown file containing a `<script>` tag, an `onerror` attribute, a remote image, and a link
- **THEN** no script runs, no request leaves for the remote image, and following the link does not replace the application

### Requirement: Disk content never runs in the application's origin
Every content response SHALL carry `X-Content-Type-Options: nosniff`, `Cross-Origin-Resource-Policy: same-origin`, and a Content-Security-Policy: a sandbox with `default-src 'none'` for images, SVG, video, and audio, and `default-src 'none'; frame-ancestors 'self'` for PDF. HTML, XML, and SVG files SHALL never be served with a type the browser would render as a page.

#### Scenario: Headers on every content response
- **WHEN** the content of any file is requested
- **THEN** the response carries `nosniff`, `Cross-Origin-Resource-Policy: same-origin`, and a Content-Security-Policy

#### Scenario: SVG with a script is only an image
- **WHEN** the content of an SVG file containing a script is opened directly in the browser
- **THEN** the response is `image/svg+xml` under a sandbox policy and the script does not run

#### Scenario: PDF is framed only by the application
- **WHEN** the content of a PDF is requested
- **THEN** its Content-Security-Policy is `default-src 'none'; frame-ancestors 'self'`

### Requirement: The viewer opens supported files in the interface
The interface SHALL open images, video and audio with seeking, PDF in the browser's built-in viewer, plain text and source code with syntax highlighting, and rendered Markdown, all without extracting anything to disk. Any other file SHALL be offered as a download.

#### Scenario: R1.13 The viewer shows the corpus files safely
- **WHEN** the owner opens the regression corpus's JPEG, MP4, MP3, PDF, Markdown file, a source file, and its Windows-1252 text file in the viewer
- **THEN** each is displayed: the image, the video and the audio playable with seeking, the PDF, the rendered Markdown, the highlighted source, and the correctly decoded text
- **AND** opening an HTML file and an SVG file with a script from the source never runs their script in the application's origin

### Requirement: Viewing is read-only and identity-checked
Content and text SHALL be read without writing to the source, and only from a file whose identity on disk still matches its entry. A request for a non-file entry, a missing entry, or a file whose identity no longer matches SHALL return 409 `invalid_entry_state`; for an entry of a source that is not online, 409 `source_offline`; for an unknown entry, 404 `not_found`.

#### Scenario: Read-only mount
- **WHEN** a source on a read-only mount is viewed
- **THEN** content and text are served and nothing on the source changes

#### Scenario: Folder content refused
- **WHEN** the content of a folder entry is requested
- **THEN** the response is 409 `invalid_entry_state`

#### Scenario: Missing file refused
- **WHEN** the content of an entry with state `missing` is requested
- **THEN** the response is 409 `invalid_entry_state`

#### Scenario: Offline source refused
- **WHEN** the text of a file on a source whose volume is not mounted is requested
- **THEN** the response is 409 `source_offline`

### Requirement: Archive members open without extraction
The content and text of a member of a completely read archive SHALL be served under the same type table and safety headers as a file, read from the archive in memory. Nothing SHALL be written to disk, not even a temporary file. When the archive's file no longer matches its indexed row, the response SHALL be 409 `invalid_entry_state` (§11.12, ADR 0007).

#### Scenario: R2.8 A photo inside the corpus's zip
- **WHEN** the owner opens a photo inside `Downloads/fotos_2005_do_pendrive.zip` in the viewer
- **THEN** the photo is shown with the type `image/jpeg` and the sandbox CSP
- **AND** the state directory, the temporary directory, and the source hold exactly the same files before and after

#### Scenario: A member of a changed archive
- **WHEN** the zip was modified on disk after it was listed, and the owner opens one of its members before a rescan
- **THEN** the response is 409 `invalid_entry_state`

#### Scenario: HTML inside an archive never runs
- **WHEN** the owner opens an `.html` member of a zip
- **THEN** it is offered as a download with `Content-Disposition: attachment`, and no script runs in the application's origin

#### Scenario: Seeking in a stored member
- **WHEN** a video is stored uncompressed inside a zip and the browser requests a byte range of it
- **THEN** the response is 206 with that range
