package api

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	"github.com/stretchr/testify/require"
)

func TestCompressionCompressesUsefulResponseTypes(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
	}{
		{name: "HTML", contentType: "text/html; charset=utf-8"},
		{name: "JavaScript", contentType: "text/javascript; charset=utf-8"},
		{name: "CSS", contentType: "text/css; charset=utf-8"},
		{name: "JSON", contentType: "application/json"},
		{name: "SVG", contentType: "image/svg+xml"},
	}
	body := strings.Repeat("compressible response content ", 100)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := compressResponses(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.Header().Set("Content-Length", "2800")
				_, _ = io.WriteString(writer, body)
			}))
			request := httptest.NewRequest(http.MethodGet, "/asset", nil)
			request.Header.Set("Accept-Encoding", "gzip, br")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, "gzip", response.Header().Get("Content-Encoding"))
			require.Empty(t, response.Header().Get("Content-Length"))
			require.Contains(t, strings.Join(response.Header().Values("Vary"), ","), "Accept-Encoding")
			require.Less(t, response.Body.Len(), len(body))
			require.Equal(t, body, gunzip(t, response.Body.Bytes()))
		})
	}
}

func TestCompressionSkipsResponsesThatWouldNotBenefit(t *testing.T) {
	tests := []struct {
		name           string
		contentType    string
		body           string
		acceptEncoding string
	}{
		{name: "small JSON", contentType: "application/json", body: `{"ok":true}`, acceptEncoding: "gzip"},
		{name: "PNG", contentType: "image/png", body: strings.Repeat("binary", 500), acceptEncoding: "gzip"},
		{name: "client opts out", contentType: "text/html", body: strings.Repeat("HTML", 500), acceptEncoding: "gzip;q=0, br"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := compressResponses(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				_, _ = io.WriteString(writer, test.body)
			}))
			request := httptest.NewRequest(http.MethodGet, "/asset", nil)
			request.Header.Set("Accept-Encoding", test.acceptEncoding)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			require.Empty(t, response.Header().Get("Content-Encoding"))
			require.Equal(t, test.body, response.Body.String())
		})
	}
}

func TestEmbeddedWebAssetsUseCompression(t *testing.T) {
	handler := New(nil, "test-task-queue", "default", "a-test-session-secret-with-at-least-32-characters")

	for _, path := range []string{"/", "/app.js", "/styles.css", "/workflows/", "/workflows/app.js"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Accept-Encoding", "gzip")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, "gzip", response.Header().Get("Content-Encoding"))
			require.NotEmpty(t, gunzip(t, response.Body.Bytes()))
		})
	}
}

func TestCompressionPreservesOtherVaryFields(t *testing.T) {
	handler := compressResponses(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		writer.Header().Set("Vary", "Cookie")
		_, _ = io.WriteString(writer, strings.Repeat("content", 500))
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	vary := strings.Join(response.Header().Values("Vary"), ",")
	require.Contains(t, vary, "Cookie")
	require.Contains(t, vary, "Accept-Encoding")
}

func TestEmbeddedPNGStaysUncompressed(t *testing.T) {
	handler := New(nil, "test-task-queue", "default", "a-test-session-secret-with-at-least-32-characters")
	request := httptest.NewRequest(http.MethodGet, "/social-preview.png", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "image/png", response.Header().Get("Content-Type"))
	require.Empty(t, response.Header().Get("Content-Encoding"))
}

func TestLambdaHTTPAPIAdapterCarriesCompressedResponses(t *testing.T) {
	handler := New(nil, "test-task-queue", "default", "a-test-session-secret-with-at-least-32-characters")
	adapter := httpadapter.NewV2(handler)

	response, err := adapter.ProxyWithContext(context.Background(), events.APIGatewayV2HTTPRequest{
		Headers: map[string]string{"accept-encoding": "gzip, br"},
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			DomainName: "example.execute-api.us-west-2.amazonaws.com",
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: http.MethodGet,
				Path:   "/app.js",
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "gzip", response.Headers["Content-Encoding"])
	require.True(t, response.IsBase64Encoded)
	compressed, err := base64.StdEncoding.DecodeString(response.Body)
	require.NoError(t, err)
	require.Contains(t, gunzip(t, compressed), "crypto.randomUUID")
}

func gunzip(t *testing.T, compressed []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(strings.NewReader(string(compressed)))
	require.NoError(t, err)
	decompressed, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	return string(decompressed)
}
