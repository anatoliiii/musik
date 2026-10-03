package static

import "embed"

//go:embed index.html app.js style.css fonts.css themes/*.css redesign.css fonts/*.woff2
var FS embed.FS
