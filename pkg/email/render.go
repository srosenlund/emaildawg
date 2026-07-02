package email

// RenderHTMLForMatrix runs the reader-mode pipeline on an email HTML body and
// returns the Matrix formatted body plus a clean plaintext fallback. Used by
// the Graph read-path (pkg/connector/graph_deliver.go), which has no Processor
// instance of its own.
func RenderHTMLForMatrix(htmlStr string, minPx int, extract bool) (formatted, plain string) {
	if minPx <= 0 {
		minPx = DefaultReaderModeMinImgPx
	}
	return toReaderModeHTML(htmlStr, readerModeOptions{MinImgPx: minPx, Extract: extract})
}
