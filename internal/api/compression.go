package api

import (
	"bytes"
	"compress/gzip"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const minimumCompressedResponseSize = 1024

func compressResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodHead || request.Header.Get("Range") != "" ||
			!acceptsGzip(request.Header.Get("Accept-Encoding")) {
			varying := &varyResponseWriter{ResponseWriter: writer}
			next.ServeHTTP(varying, request)
			addVary(writer.Header(), "Accept-Encoding")
			return
		}

		compressed := &compressionResponseWriter{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(compressed, request)
		_ = compressed.close()
	})
}

type compressionResponseWriter struct {
	http.ResponseWriter
	buffer      bytes.Buffer
	gzipWriter  *gzip.Writer
	status      int
	wroteHeader bool
	committed   bool
	compressed  bool
}

type varyResponseWriter struct {
	http.ResponseWriter
}

func (writer *varyResponseWriter) WriteHeader(status int) {
	addVary(writer.Header(), "Accept-Encoding")
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *varyResponseWriter) Write(body []byte) (int, error) {
	addVary(writer.Header(), "Accept-Encoding")
	return writer.ResponseWriter.Write(body)
}

func (writer *varyResponseWriter) Flush() {
	addVary(writer.Header(), "Accept-Encoding")
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *varyResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *compressionResponseWriter) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.status = status
	writer.wroteHeader = true
}

func (writer *compressionResponseWriter) Write(body []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if writer.committed {
		if writer.compressed {
			return writer.gzipWriter.Write(body)
		}
		return writer.ResponseWriter.Write(body)
	}

	written, err := writer.buffer.Write(body)
	if err != nil || writer.buffer.Len() < minimumCompressedResponseSize {
		return written, err
	}
	if err := writer.commit(true); err != nil {
		return 0, err
	}
	return written, nil
}

func (writer *compressionResponseWriter) Flush() {
	if !writer.committed {
		_ = writer.commit(false)
	}
	if writer.compressed {
		_ = writer.gzipWriter.Flush()
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *compressionResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *compressionResponseWriter) close() error {
	if !writer.committed {
		if err := writer.commit(true); err != nil {
			return err
		}
	}
	if writer.compressed {
		return writer.gzipWriter.Close()
	}
	return nil
}

func (writer *compressionResponseWriter) commit(allowCompression bool) error {
	if writer.committed {
		return nil
	}
	writer.committed = true

	if writer.Header().Get("Content-Type") == "" && writer.buffer.Len() > 0 {
		writer.Header().Set("Content-Type", http.DetectContentType(writer.buffer.Bytes()))
	}
	addVary(writer.Header(), "Accept-Encoding")
	writer.compressed = allowCompression && writer.buffer.Len() >= minimumCompressedResponseSize &&
		responseCanBeCompressed(writer.status, writer.Header())
	if writer.compressed {
		writer.Header().Del("Content-Length")
		writer.Header().Set("Content-Encoding", "gzip")
	}
	writer.ResponseWriter.WriteHeader(writer.status)

	if writer.compressed {
		gzipWriter, err := gzip.NewWriterLevel(writer.ResponseWriter, gzip.BestSpeed)
		if err != nil {
			return err
		}
		writer.gzipWriter = gzipWriter
		_, err = writer.gzipWriter.Write(writer.buffer.Bytes())
		writer.buffer.Reset()
		return err
	}
	_, err := writer.ResponseWriter.Write(writer.buffer.Bytes())
	writer.buffer.Reset()
	return err
}

func responseCanBeCompressed(status int, header http.Header) bool {
	if status < 200 || status == http.StatusNoContent || status == http.StatusNotModified {
		return false
	}
	if header.Get("Content-Encoding") != "" || header.Get("Content-Range") != "" {
		return false
	}
	for _, directive := range strings.Split(header.Get("Cache-Control"), ",") {
		if strings.EqualFold(strings.TrimSpace(directive), "no-transform") {
			return false
		}
	}

	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		return false
	}
	if strings.HasPrefix(mediaType, "text/") || strings.HasSuffix(mediaType, "+json") ||
		strings.HasSuffix(mediaType, "+xml") {
		return true
	}
	switch mediaType {
	case "application/json", "application/javascript", "application/xml", "application/xhtml+xml", "image/svg+xml":
		return true
	default:
		return false
	}
}

func acceptsGzip(value string) bool {
	wildcardQuality := -1.0
	for _, option := range strings.Split(value, ",") {
		parts := strings.Split(option, ";")
		encoding := strings.ToLower(strings.TrimSpace(parts[0]))
		quality := 1.0
		for _, parameter := range parts[1:] {
			name, raw, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || !strings.EqualFold(name, "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				quality = 0
			} else {
				quality = parsed
			}
		}
		if encoding == "gzip" {
			return quality > 0
		}
		if encoding == "*" {
			wildcardQuality = quality
		}
	}
	return wildcardQuality > 0
}

func addVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, field := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(field), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
