package static

import "embed"

//go:embed index.html app.js style.css redesign.css fonts.css fonts/*.woff2
var FS embed.FS
