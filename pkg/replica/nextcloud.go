package replica

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"chronicle-server/pkg/config"
)

type NextcloudProvider struct {
	cfg          *config.Config
	rootSegments []string
}

func NewNextcloudProvider(cfg *config.Config) (*NextcloudProvider, error) {
	segments := safeSegments(cfg.Nextcloud.StorageDir, false)
	return &NextcloudProvider{
		cfg:          cfg,
		rootSegments: segments,
	}, nil
}

func (p *NextcloudProvider) Name() string {
	return "nextcloud"
}

func safeSegments(p string, allowEmpty bool) []string {
	parts := strings.Split(p, "/")
	var segments []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		if part == "." || part == ".." {
			// Skip or throw error? Original Node throws error
			panic(fmt.Sprintf("Invalid Nextcloud path part: %s in %s", part, p))
		}
		segments = append(segments, part)
	}
	if len(segments) == 0 && !allowEmpty {
		panic(fmt.Sprintf("Invalid Nextcloud path: %s", p))
	}
	return segments
}

func encodeSegments(segments []string) string {
	var encoded []string
	for _, s := range segments {
		encoded = append(encoded, url.PathEscape(s))
	}
	return strings.Join(encoded, "/")
}

func (p *NextcloudProvider) userBaseUrl() string {
	return fmt.Sprintf("%s/remote.php/dav/files/%s", p.cfg.Nextcloud.Url, url.PathEscape(p.cfg.Nextcloud.User))
}

func (p *NextcloudProvider) rootUrl() string {
	return fmt.Sprintf("%s/%s", p.userBaseUrl(), encodeSegments(p.rootSegments))
}

func (p *NextcloudProvider) authHeader() string {
	creds := fmt.Sprintf("%s:%s", p.cfg.Nextcloud.User, p.cfg.Nextcloud.Pass)
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
}

func (p *NextcloudProvider) urlFor(key string) string {
	return fmt.Sprintf("%s/%s", p.rootUrl(), encodeSegments(safeSegments(key, false)))
}

func (p *NextcloudProvider) request(ctx context.Context, method string, urlStr string, body []byte, headers map[string]string) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", p.authHeader())
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: 20 * time.Second,
	}
	return client.Do(req)
}

func (p *NextcloudProvider) Initialize(ctx context.Context) error {
	endpoint, err := url.Parse(p.cfg.Nextcloud.Url)
	if err != nil {
		return fmt.Errorf("invalid Nextcloud URL: %w", err)
	}

	if endpoint.Scheme != "https" && !p.cfg.Nextcloud.AllowInsecureHttp {
		return errors.New("refusing insecure Nextcloud endpoint; set NEXTCLOUD_ALLOW_INSECURE_HTTP=true only for a trusted LAN")
	}

	current := p.userBaseUrl()
	for _, segment := range p.rootSegments {
		current += "/" + url.PathEscape(segment)
		resp, err := p.request(ctx, "MKCOL", current, nil, nil)
		if err != nil {
			return err
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("Nextcloud could not create NC_DIR=%s: %s", p.cfg.Nextcloud.StorageDir, resp.Status)
		}
	}

	return nil
}

func (p *NextcloudProvider) Put(ctx context.Context, key string, content []byte, opts ReplicaPutOptions) error {
	segments := safeSegments(key, false)
	if len(segments) > 1 {
		dirPath := strings.Join(segments[:len(segments)-1], "/")
		if err := p.EnsureDir(ctx, dirPath); err != nil {
			return err
		}
	}

	headers := map[string]string{
		"OC-Checksum":            "SHA256:" + opts.Checksum,
		"X-Chronicle-Generation": strconv.Itoa(opts.Generation),
	}
	if opts.ContentType != "" {
		headers["Content-Type"] = opts.ContentType
	}

	resp, err := p.request(ctx, "PUT", p.urlFor(key), content, headers)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Nextcloud PUT %s failed: %s", key, resp.Status)
	}
	return nil
}

func (p *NextcloudProvider) Head(ctx context.Context, key string) (*ReplicaObjectMetadata, error) {
	resp, err := p.request(ctx, "HEAD", p.urlFor(key), nil, nil)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Nextcloud HEAD %s failed: %s", key, resp.Status)
	}

	checksumHeader := resp.Header.Get("OC-Checksum")
	var checksum *string
	if checksumHeader != "" {
		c := strings.ToLower(strings.TrimPrefix(checksumHeader, "SHA256:"))
		checksum = &c
	}

	var gen *int
	if rawGen := resp.Header.Get("X-Chronicle-Generation"); rawGen != "" {
		if g, errScan := strconv.Atoi(rawGen); errScan == nil {
			gen = &g
		}
	}

	var size *int64
	if rawSize := resp.Header.Get("Content-Length"); rawSize != "" {
		if sz, errScan := strconv.ParseInt(rawSize, 10, 64); errScan == nil {
			size = &sz
		}
	}

	var updatedAt *int64
	if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
		if t, errScan := time.Parse(time.RFC1123, lastMod); errScan == nil {
			millis := t.UnixNano() / int64(time.Millisecond)
			updatedAt = &millis
		}
	}

	etag := resp.Header.Get("ETag")
	var etagVal *string
	if etag != "" {
		etagVal = &etag
	}

	var contentTypeVal *string
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		contentTypeVal = &ct
	}

	return &ReplicaObjectMetadata{
		Key:         key,
		Size:        size,
		ContentType: contentTypeVal,
		Checksum:    checksum,
		Generation:  gen,
		ETag:        etagVal,
		UpdatedAt:   updatedAt,
	}, nil
}

func (p *NextcloudProvider) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := p.request(ctx, "GET", p.urlFor(key), nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Nextcloud GET %s failed: %s", key, resp.Status)
	}

	return io.ReadAll(resp.Body)
}

func (p *NextcloudProvider) Delete(ctx context.Context, key string) error {
	resp, err := p.request(ctx, "DELETE", p.urlFor(key), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("Nextcloud DELETE %s failed: %s", key, resp.Status)
	}
	return nil
}

var responseRegex = regexp.MustCompile(`(?i)<(?:[A-Za-z][\w.-]*:)?response\b[\s\S]*?<\/(?:[A-Za-z][\w.-]*:)?response>`)
var hrefRegex = regexp.MustCompile(`(?i)<(?:[A-Za-z][\w.-]*:)?href\b[^>]*>([\s\S]*?)<\/(?:[A-Za-z][\w.-]*:)?href>`)
var collectionRegex = regexp.MustCompile(`(?i)<(?:[A-Za-z][\w.-]*:)?collection\b`)

type webdavEntry struct {
	key        string
	collection bool
}

func (p *NextcloudProvider) readDirectory(ctx context.Context, relativePath string) ([]webdavEntry, error) {
	var urlStr string
	if relativePath != "" {
		urlStr = p.urlFor(relativePath)
	} else {
		urlStr = p.rootUrl()
	}

	headers := map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml",
	}

	resp, err := p.request(ctx, "PROPFIND", urlStr, nil, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Nextcloud PROPFIND %s failed: %s", relativePath, resp.Status)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	rootUrlParsed, err := url.Parse(p.rootUrl())
	if err != nil {
		return nil, err
	}
	rootPath := strings.TrimRight(rootUrlParsed.Path, "/")

	var entries []webdavEntry
	blocks := responseRegex.FindAllString(string(bodyBytes), -1)

	for _, block := range blocks {
		hrefMatches := hrefRegex.FindStringSubmatch(block)
		if len(hrefMatches) < 2 {
			continue
		}

		hrefDecoded := html.UnescapeString(strings.TrimSpace(hrefMatches[1]))
		hrefUrl, err := url.Parse(hrefDecoded)
		if err != nil {
			continue
		}

		entryPath := strings.TrimRight(hrefUrl.Path, "/")
		if entryPath == rootPath || !strings.HasPrefix(entryPath, rootPath+"/") {
			continue
		}

		key := entryPath[len(rootPath)+1:]
		if key == "" {
			continue
		}

		isCollection := collectionRegex.MatchString(block)
		entries = append(entries, webdavEntry{
			key:        key,
			collection: isCollection,
		})
	}

	return entries, nil
}

func (p *NextcloudProvider) List(ctx context.Context, prefix string) ([]ReplicaObjectMetadata, error) {
	normalizedPrefix := strings.Join(safeSegments(prefix, true), "/")
	root := strings.TrimRight(normalizedPrefix, "/")

	queue := []string{root}
	visited := make(map[string]bool)
	var objects []ReplicaObjectMetadata

	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]

		if visited[dir] {
			continue
		}
		visited[dir] = true

		entries, err := p.readDirectory(ctx, dir)
		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			if entry.collection {
				queue = append(queue, entry.key)
			} else if root == "" || strings.HasPrefix(entry.key, root) {
				objects = append(objects, ReplicaObjectMetadata{
					Key: entry.key,
				})
			}
		}
	}

	return objects, nil
}

func (p *NextcloudProvider) EnsureDir(ctx context.Context, path string) error {
	segments := safeSegments(path, true)
	current := p.rootUrl()
	for _, segment := range segments {
		current += "/" + url.PathEscape(segment)
		resp, err := p.request(ctx, "MKCOL", current, nil, nil)
		if err != nil {
			return err
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("Nextcloud MKCOL %s failed: %s", path, resp.Status)
		}
	}
	return nil
}

func (p *NextcloudProvider) Close() error {
	return nil
}
