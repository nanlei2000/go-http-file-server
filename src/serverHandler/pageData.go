package serverHandler

import (
	"../util"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
)

type pathEntry struct {
	Name string
	Path string
}

type pageData struct {
	Scheme    string
	Host      string
	Path      string
	Paths     []*pathEntry
	File      *os.File
	Item      os.FileInfo
	SubItems  []os.FileInfo
	CanUpload bool
	Errors    []error
}

func getScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https:"
	} else {
		return "http:"
	}
}

func getPathEntries(path string) []*pathEntry {
	var pathParts []string
	if len(path) > 0 {
		pathParts = strings.Split(path, "/")
	} else {
		pathParts = []string{}
	}

	escapedPathParts := make([]string, len(pathParts))
	for i, length := 0, len(pathParts); i < length; i++ {
		escapedPathParts[i] = url.PathEscape(pathParts[i])
	}

	pathEntries := make([]*pathEntry, 0, len(pathParts))
	for i, part := range pathParts {
		pathEntries = append(pathEntries, &pathEntry{
			Name: part,
			Path: "/" + strings.Join(escapedPathParts[:i+1], "/"),
		})
	}

	return pathEntries
}

func (h *handler) stat(requestPath string) (file *os.File, item os.FileInfo, err error) {
	fsPath := path.Clean(h.root + requestPath)

	file, err = os.Open(fsPath)
	if err != nil {
		return
	}

	item, err = file.Stat()
	if err != nil {
		return
	}

	return
}

func (h *handler) readdir(file *os.File, item os.FileInfo) (subItems []os.FileInfo, errs []error) {
	if file == nil || item == nil || !item.IsDir() {
		return
	}

	var err error
	subItems, err = file.Readdir(0)
	if err != nil {
		errs = append(errs, err)
		return
	}

	return
}

func (h *handler) mergeAlias(rawRequestPath string, subItems *[]os.FileInfo) []error {
	errs := []error{}

	for aliasUrlPath, aliasFsPath := range h.aliases {
		if len(aliasUrlPath) <= len(rawRequestPath) {
			continue
		}

		suffixIndex := len(rawRequestPath)
		aliasPrefix := aliasUrlPath[:suffixIndex]
		aliasSuffix := aliasUrlPath[suffixIndex:]

		if aliasPrefix != rawRequestPath {
			continue
		}

		if len(aliasPrefix) != 1 && aliasSuffix[0] != '/' {
			// aliasUrlPath doesn't cover the whole directory name
			// e.g:
			// rawRequestPath == "/abc/def/ghi"
			// aliasPrefix == "/abc/de"
			continue
		}
		if aliasSuffix[0] == '/' {
			aliasSuffix = aliasSuffix[1:]
		}

		slashIndex := strings.Index(aliasSuffix, "/")
		var nextName string
		if slashIndex >= 0 {
			nextName = aliasSuffix[:slashIndex]
		} else {
			nextName = aliasSuffix
		}

		var aliasSubItem os.FileInfo
		if path.Dir(aliasUrlPath) == rawRequestPath { // reached second deepest path of alias
			var err error
			aliasSubItem, err = os.Stat(aliasFsPath)
			if err == nil {
				aliasSubItem = NewRenamedFileInfo(nextName, aliasSubItem)
			} else {
				errs = append(errs, err)
				aliasSubItem = NewFakeFileInfo(nextName, true)
			}
		} else {
			aliasSubItem = NewFakeFileInfo(nextName, true)
		}

		replaced := false
		for i, subItem := range *subItems {
			if subItem.Name() == nextName {
				(*subItems)[i] = aliasSubItem
				replaced = true
				break
			}
		}

		if !replaced {
			*subItems = append(*subItems, aliasSubItem)
		}
	}

	return errs
}

func (h *handler) FilterItems(items []os.FileInfo) []os.FileInfo {
	if h.shows == nil &&
		h.showDirs == nil &&
		h.showFiles == nil &&
		h.hides == nil &&
		h.hideDirs == nil &&
		h.hideFiles == nil {
		return items
	}

	filtered := make([]os.FileInfo, 0, len(items))

	for _, item := range items {
		shouldShow := true
		if h.shows != nil {
			shouldShow = shouldShow && h.shows.MatchString(item.Name())
		}
		if h.showDirs != nil && item.IsDir() {
			shouldShow = shouldShow && h.showDirs.MatchString(item.Name())
		}
		if h.showFiles != nil && !item.IsDir() {
			shouldShow = shouldShow && h.showFiles.MatchString(item.Name())
		}

		shouldHide := false
		if h.hides != nil {
			shouldHide = shouldHide || h.hides.MatchString(item.Name())
		}
		if h.hideDirs != nil && item.IsDir() {
			shouldHide = shouldHide || h.hideDirs.MatchString(item.Name())
		}
		if h.hideFiles != nil && !item.IsDir() {
			shouldHide = shouldHide || h.hideFiles.MatchString(item.Name())
		}

		if shouldShow && !shouldHide {
			filtered = append(filtered, item)
		}
	}

	return filtered
}

func sortSubItems(subItems []os.FileInfo) {
	sort.Slice(
		subItems,
		func(i, j int) bool {
			itemI := subItems[i]
			itemJ := subItems[j]

			isDirI := itemI.IsDir()
			isDirJ := itemJ.IsDir()

			if (isDirI && isDirJ) || (!isDirI && !isDirJ) {
				return itemI.Name() < itemJ.Name()
			}

			return isDirI
		},
	)
}

func (h *handler) getPageData(r *http.Request) (data *pageData, notFound, internalError bool) {
	rawRequestPath := util.CleanUrlPath(r.URL.Path)
	requestPath := util.CleanUrlPath(rawRequestPath[len(h.urlPrefix):]) // strip url prefix path
	errs := []error{}

	scheme := getScheme(r)

	relPath := rawRequestPath[1:]
	pathEntries := getPathEntries(relPath)

	file, item, _statErr := h.stat(requestPath)
	if _statErr != nil {
		errs = append(errs, _statErr)
		notFound = os.IsNotExist(_statErr)
		internalError = internalError || !notFound
	}

	canUpload := item != nil && h.uploads[rawRequestPath]
	if canUpload && r.Method == "POST" {
		_uploadErrs := h.saveUploadFiles(requestPath, r)
		errs = append(errs, _uploadErrs...)
		internalError = internalError || len(_uploadErrs) > 0
	}

	subItems, _readdirErrs := h.readdir(file, item)
	errs = append(errs, _readdirErrs...)
	internalError = internalError || len(_readdirErrs) > 0

	_mergeErrs := h.mergeAlias(rawRequestPath, &subItems)
	errs = append(errs, _mergeErrs...)
	internalError = internalError || len(_mergeErrs) > 0

	subItems = h.FilterItems(subItems)
	sortSubItems(subItems)

	data = &pageData{
		Scheme:    scheme,
		Host:      r.Host,
		Path:      relPath,
		Paths:     pathEntries,
		File:      file,
		Item:      item,
		SubItems:  subItems,
		CanUpload: canUpload,
		Errors:    errs,
	}

	return
}