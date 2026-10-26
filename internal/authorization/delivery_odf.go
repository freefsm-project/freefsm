package authorization

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

const odfOfficeNamespace = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
const odfManifestNamespace = "urn:oasis:names:tc:opendocument:xmlns:manifest:1.0"

func validDeliveryODF(entries map[string][]byte, ext, want string) bool {
	if !validDeliveryODFManifest(entries, want) {
		return false
	}
	kind := "text"
	if ext == ".ods" {
		kind = "spreadsheet"
	}
	decoder := xml.NewDecoder(bytes.NewReader(entries["content.xml"]))
	depth, bodies, payloads := 0, 0, 0
	rootSeen, inBody := false, false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return rootSeen && depth == 0 && bodies == 1 && payloads == 1
		}
		if err != nil {
			return false
		}
		switch node := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if rootSeen || node.Name != (xml.Name{Space: odfOfficeNamespace, Local: "document-content"}) {
					return false
				}
				rootSeen = true
			}
			if node.Name == (xml.Name{Space: odfOfficeNamespace, Local: "body"}) {
				if depth != 2 || bodies != 0 {
					return false
				}
				bodies++
				inBody = true
			}
			if inBody && depth == 3 {
				if payloads != 0 || node.Name != (xml.Name{Space: odfOfficeNamespace, Local: kind}) {
					return false
				}
				payloads++
			}
			if node.Name.Space == odfOfficeNamespace && (node.Name.Local == "text" || node.Name.Local == "spreadsheet") && (!inBody || depth != 3) {
				return false
			}
		case xml.EndElement:
			if inBody && depth == 2 {
				inBody = false
			}
			depth--
		case xml.CharData:
			if (depth == 0 || inBody && depth == 2) && strings.TrimSpace(string(node)) != "" {
				return false
			}
		}
	}
}

func validDeliveryODFManifest(entries map[string][]byte, want string) bool {
	decoder := xml.NewDecoder(bytes.NewReader(entries["META-INF/manifest.xml"]))
	depth := 0
	rootSeen, packageType, content := false, false, false
	paths := map[string]bool{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return rootSeen && depth == 0 && packageType && content
		}
		if err != nil {
			return false
		}
		switch node := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if rootSeen || node.Name != (xml.Name{Space: odfManifestNamespace, Local: "manifest"}) {
					return false
				}
				rootSeen = true
				continue
			}
			if depth != 2 || node.Name != (xml.Name{Space: odfManifestNamespace, Local: "file-entry"}) {
				return false
			}
			path, media := "", ""
			pathSeen, mediaSeen := false, false
			for _, attr := range node.Attr {
				if attr.Name.Space != odfManifestNamespace {
					continue
				}
				switch attr.Name.Local {
				case "full-path":
					if pathSeen {
						return false
					}
					pathSeen = true
					path = attr.Value
				case "media-type":
					if mediaSeen {
						return false
					}
					mediaSeen = true
					media = attr.Value
				}
			}
			if !pathSeen || !mediaSeen || path == "" || paths[path] {
				return false
			}
			paths[path] = true
			switch path {
			case "/":
				if media != want {
					return false
				}
				packageType = true
			case "content.xml":
				if media != "text/xml" && media != "application/xml" {
					return false
				}
				content = true
			default:
				if !strings.HasSuffix(path, "/") {
					if _, ok := entries[path]; !ok {
						return false
					}
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if strings.TrimSpace(string(node)) != "" {
				return false
			}
		}
	}
}
