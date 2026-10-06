package viewer

import "bytes"

// Content security policies of the type table (design D12). Images, SVG,
// video, audio, and downloads run sandboxed with nothing allowed; SVG also
// keeps its inline styles. PDF is not sandboxed, because Chrome refuses to
// show a PDF in a sandboxed frame, and only the application may frame it.
const (
	sandboxCSP = "sandbox; default-src 'none'"
	svgCSP     = "sandbox; default-src 'none'; style-src 'unsafe-inline'"
	pdfCSP     = "default-src 'none'; frame-ancestors 'self'"
)

// contentType is how /content serves one kind of file.
type contentType struct {
	mime string
	csp  string
	// download sends the file as an attachment.
	download bool
}

// download is every file the table does not name: never a type a browser
// renders, so HTML, XML, scripts, and executables are only saved.
var download = contentType{mime: "application/octet-stream", csp: sandboxCSP, download: true}

// contentTypes is Precious's own type table, by lowercased extension. The
// type is never inferred from the content (§11.12).
var contentTypes = map[string]contentType{
	"jpg":  {mime: "image/jpeg", csp: sandboxCSP},
	"jpeg": {mime: "image/jpeg", csp: sandboxCSP},
	"jpe":  {mime: "image/jpeg", csp: sandboxCSP},
	"png":  {mime: "image/png", csp: sandboxCSP},
	"gif":  {mime: "image/gif", csp: sandboxCSP},
	"webp": {mime: "image/webp", csp: sandboxCSP},
	"avif": {mime: "image/avif", csp: sandboxCSP},
	"bmp":  {mime: "image/bmp", csp: sandboxCSP},
	"svg":  {mime: "image/svg+xml", csp: svgCSP},
	"mp4":  {mime: "video/mp4", csp: sandboxCSP},
	// M4V is an MP4 container; browsers play it under the MP4 type.
	"m4v":  {mime: "video/mp4", csp: sandboxCSP},
	"webm": {mime: "video/webm", csp: sandboxCSP},
	"mov":  {mime: "video/quicktime", csp: sandboxCSP},
	"mp3":  {mime: "audio/mpeg", csp: sandboxCSP},
	"m4a":  {mime: "audio/mp4", csp: sandboxCSP},
	"aac":  {mime: "audio/aac", csp: sandboxCSP},
	"ogg":  {mime: "audio/ogg", csp: sandboxCSP},
	"opus": {mime: "audio/ogg", csp: sandboxCSP},
	"wav":  {mime: "audio/wav", csp: sandboxCSP},
	"flac": {mime: "audio/flac", csp: sandboxCSP},
	"pdf":  {mime: "application/pdf", csp: pdfCSP},
}

// typeOf returns how /content serves the file name.
func typeOf(name []byte) contentType {
	if t, ok := contentTypes[extOf(name)]; ok {
		return t
	}
	return download
}

// languages maps a lowercased extension to its highlight.js language, the
// syntax hint of /text.
var languages = map[string]string{
	"bas": "vbnet", "bat": "dos", "c": "c", "cc": "cpp", "cfg": "ini", "cjs": "javascript", "clj": "clojure",
	"cmd": "dos", "conf": "ini", "cpp": "cpp", "cs": "csharp", "css": "css", "cxx": "cpp", "dart": "dart",
	"diff": "diff", "erl": "erlang", "ex": "elixir", "exs": "elixir", "go": "go", "groovy": "groovy",
	"h": "c", "hh": "cpp", "hpp": "cpp", "hs": "haskell", "htm": "xml", "html": "xml", "ini": "ini",
	"java": "java", "js": "javascript", "json": "json", "jsx": "javascript", "kt": "kotlin", "less": "less",
	"lua": "lua", "markdown": "markdown", "md": "markdown", "mjs": "javascript", "pas": "delphi",
	"patch": "diff", "php": "php", "pl": "perl", "pm": "perl", "ps1": "powershell", "py": "python",
	"pyw": "python", "r": "r", "rb": "ruby", "rs": "rust", "scala": "scala", "scss": "scss", "sh": "bash",
	"sql": "sql", "svg": "xml", "swift": "swift", "tex": "latex", "toml": "ini", "ts": "typescript",
	"tsx": "typescript", "vb": "vbnet", "vbs": "vbscript", "xml": "xml", "xsl": "xml", "yaml": "yaml",
	"yml": "yaml", "zsh": "bash",
}

// languageOf returns the syntax hint for the file name, or nil.
func languageOf(name []byte) *string {
	if l, ok := languages[extOf(name)]; ok {
		return &l
	}
	return nil
}

// isMarkdown reports whether the client renders the file name as Markdown.
func isMarkdown(name []byte) bool {
	switch extOf(name) {
	case "md", "markdown":
		return true
	}
	return false
}

// extOf is the ASCII-lowercased text after the last '.' of name, or "" when
// there is none or the only '.' is the first byte (a dotfile). An extension
// longer than every table key is "".
func extOf(name []byte) string {
	i := bytes.LastIndexByte(name, '.')
	if i <= 0 || len(name)-i-1 > len("markdown") {
		return ""
	}
	var buf [len("markdown")]byte
	ext := buf[:0]
	for _, c := range name[i+1:] {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		ext = append(ext, c)
	}
	return string(ext)
}
