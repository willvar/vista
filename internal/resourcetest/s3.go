// Package resourcetest provides an HTTP fixture for storage contract tests.
// It does not validate S3 signatures; real-provider checks remain separate.
package resourcetest

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type S3 struct {
	Server   *httptest.Server
	mu       sync.Mutex
	Objects  map[string][]byte
	Requests atomic.Int64
	Hook     func(*http.Request) int
}

type Owner interface {
	Helper()
	Cleanup(func())
}

func NewS3(t Owner) *S3 {
	t.Helper()
	s := &S3{Objects: map[string][]byte{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Server.Close)
	return s
}
func (s *S3) SetHook(hook func(*http.Request) int) { s.mu.Lock(); defer s.mu.Unlock(); s.Hook = hook }
func (s *S3) Count() int                           { s.mu.Lock(); defer s.mu.Unlock(); return len(s.Objects) }
func (s *S3) Corrupt(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value := s.Objects[key]; len(value) > 10 {
		value[10] ^= 1
	}
}
func (s *S3) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for key := range s.Objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func (s *S3) Raw(key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.Objects[key])
}
func (s *S3) serve(w http.ResponseWriter, r *http.Request) {
	s.Requests.Add(1)
	s.mu.Lock()
	hook := s.Hook
	s.mu.Unlock()
	if hook != nil {
		if status := hook(r); status != 0 {
			s.fail(w, status, "AccessDenied")
			return
		}
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	q := r.URL.Query()
	if q.Has("location") {
		s.xml(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
		return
	}
	if q.Has("versioning") {
		s.xml(w, `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`)
		return
	}
	if q.Has("uploads") {
		s.xml(w, `<ListMultipartUploadsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Bucket>resources</Bucket><IsTruncated>false</IsTruncated></ListMultipartUploadsResult>`)
		return
	}
	if r.Method == http.MethodGet && key == "" {
		type item struct {
			Key          string
			LastModified string
			ETag         string
			Size         int
			StorageClass string
		}
		result := struct {
			XMLName     xml.Name `xml:"ListBucketResult"`
			Name        string
			Prefix      string
			IsTruncated bool
			Contents    []item
		}{Name: "resources", Prefix: q.Get("prefix")}
		s.mu.Lock()
		for k, v := range s.Objects {
			if strings.HasPrefix(k, q.Get("prefix")) {
				sum := md5.Sum(v)
				result.Contents = append(result.Contents, item{Key: k, LastModified: time.Now().UTC().Format(time.RFC3339), ETag: hex.EncodeToString(sum[:]), Size: len(v), StorageClass: "STANDARD"})
			}
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(result)
		return
	}
	if r.Method == http.MethodPost && q.Has("delete") {
		var request struct {
			Objects []struct{ Key string } `xml:"Object"`
		}
		if xml.NewDecoder(r.Body).Decode(&request) != nil {
			s.fail(w, 400, "MalformedXML")
			return
		}
		var response struct {
			XMLName xml.Name `xml:"DeleteResult"`
			Deleted []struct{ Key string }
		}
		s.mu.Lock()
		for _, obj := range request.Objects {
			delete(s.Objects, obj.Key)
			response.Deleted = append(response.Deleted, struct{ Key string }{obj.Key})
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(response)
		return
	}
	if r.Method == http.MethodHead && key == "" {
		w.WriteHeader(200)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var body io.Reader = r.Body
		if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
			body = httputil.NewChunkedReader(r.Body)
		}
		data, err := io.ReadAll(io.LimitReader(body, 32<<20))
		if err != nil {
			s.fail(w, 400, "InvalidRequest")
			return
		}
		s.mu.Lock()
		s.Objects[key] = data
		s.mu.Unlock()
		sum := md5.Sum(data)
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sum))
		w.WriteHeader(200)
	case http.MethodGet, http.MethodHead:
		s.mu.Lock()
		data, ok := s.Objects[key]
		data = bytes.Clone(data)
		s.mu.Unlock()
		if !ok {
			s.fail(w, 404, "NoSuchKey")
			return
		}
		sum := md5.Sum(data)
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sum))
		w.Header().Set("Content-Type", "application/vnd.dofs.encrypted")
		http.ServeContent(w, r, "object", time.Unix(1700000000, 0), bytes.NewReader(data))
	case http.MethodDelete:
		s.mu.Lock()
		delete(s.Objects, key)
		s.mu.Unlock()
		w.WriteHeader(204)
	default:
		s.fail(w, 400, "InvalidRequest")
	}
}
func (s *S3) xml(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, body)
}
func (s *S3) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>Fixture request failure</Message></Error>", code)
}

func (s *S3) PutUntracked(key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Objects[key] = bytes.Clone(value)
}
