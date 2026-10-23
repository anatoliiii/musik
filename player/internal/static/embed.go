package static

import "embed"

//go:embed index.html mozgolika.html app.js style.css redesign.css fonts.css themes/*.css fonts/*.woff2 icons/*.png
var FS embed.FS
