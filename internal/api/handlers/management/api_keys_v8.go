package management

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

// decodeV8APIKeyEntries reads access.api-keys from the v8 tree.
func decodeV8APIKeyEntries(root *yaml.Node, parts []string) ([]config.APIKeyEntry, error) {
	node := configV8Node(root, parts)
	if node == nil || node.Tag == "!!null" {
		return []config.APIKeyEntry{}, nil
	}
	var entries []config.APIKeyEntry
	if err := node.Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// updateV8APIKeys handles PUT, PATCH, and DELETE for the client key list while
// keeping the generic v8 validation and save pipeline.
func (h *Handler) updateV8APIKeys(c *gin.Context, root *yaml.Node, parts []string) bool {
	entries, err := decodeV8APIKeyEntries(root, parts)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return false
	}

	switch c.Request.Method {
	case http.MethodDelete:
		index, hasIndex := queryIndex(c)
		value := c.Query("value")
		if !hasIndex && strings.TrimSpace(value) == "" {
			if !deleteConfigV8Path(root, parts) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
				return false
			}
			return true
		}
		next, ok := deleteAPIKeyEntries(entries, index, value)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing index or value"})
			return false
		}
		entries = next
	case http.MethodPatch:
		body, ok := readAPIKeysBody(c)
		if !ok {
			return false
		}
		bodyPatch, errParse := parseAPIKeyPatch(body)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return false
		}
		next, errPatch := applyAPIKeyEntryPatch(entries, bodyPatch)
		if errPatch != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errPatch.Error()})
			return false
		}
		entries = next
	default:
		body, ok := readAPIKeysBody(c)
		if !ok {
			return false
		}
		next, errParse := parseAPIKeyEntryList(body)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return false
		}
		entries = normalizeAPIKeyEntries(next)
	}

	if err = setV8APIKeyEntries(root, parts, entries); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return false
	}
	return true
}

func readAPIKeysBody(c *gin.Context) ([]byte, bool) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), gin.H{"error": "invalid_body"})
		return nil, false
	}
	return body, true
}

// setV8APIKeyEntries replaces the access.api-keys sequence while keeping
// unrelated document structure intact.
func setV8APIKeyEntries(root *yaml.Node, parts []string, entries []config.APIKeyEntry) error {
	if root == nil || root.Kind != yaml.MappingNode || len(parts) == 0 {
		return fmt.Errorf("invalid target path")
	}
	rendered, err := yaml.Marshal(entries)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(rendered, &doc); err != nil || len(doc.Content) == 0 {
		return err
	}
	value := doc.Content[0]

	dst := root
	for _, part := range parts[:len(parts)-1] {
		next := configV8Node(dst, []string{part})
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			dst.Content = append(dst.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, next)
		}
		if next.Kind != yaml.MappingNode {
			return fmt.Errorf("invalid target path")
		}
		dst = next
	}

	key := parts[len(parts)-1]
	for i := 0; i+1 < len(dst.Content); i += 2 {
		if dst.Content[i].Value == key {
			dst.Content[i+1] = value
			return nil
		}
	}
	dst.Content = append(dst.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
	return nil
}
