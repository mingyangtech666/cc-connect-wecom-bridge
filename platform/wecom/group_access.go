package wecom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

type groupAllowlist struct {
	mu     sync.RWMutex
	path   string
	groups map[string]bool
}

func loadGroupAllowlist(opts map[string]any, botID string) (*groupAllowlist, error) {
	setting, exists := opts["group_allowlist"]
	if !exists {
		return nil, nil
	}
	enabled, ok := setting.(bool)
	if !ok {
		return nil, fmt.Errorf("wecom-ws: group_allowlist must be a boolean")
	}
	if !enabled {
		return nil, nil
	}
	dataDir, _ := opts["cc_data_dir"].(string)
	project, _ := opts["cc_project"].(string)
	if dataDir == "" || project == "" {
		return nil, fmt.Errorf("wecom-ws: group_allowlist requires cc_data_dir and cc_project")
	}
	identity := sha256.Sum256([]byte(project + "\x00" + botID))
	store := &groupAllowlist{
		path:   filepath.Join(dataDir, "wecom_group_allowlists", hex.EncodeToString(identity[:16])+".json"),
		groups: make(map[string]bool),
	}
	data, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wecom-ws: read group allowlist: %w", err)
	}
	var saved struct {
		Groups []string `json:"groups"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("wecom-ws: parse group allowlist: %w", err)
	}
	for _, id := range saved.Groups {
		if id = strings.TrimSpace(id); id != "" {
			store.groups[id] = true
		}
	}
	return store, nil
}

func (p *WSPlatform) GroupAccessEnabled() bool { return p.groupAllowlist != nil }

func (p *WSPlatform) GroupAccess(replyCtx any) (string, bool) {
	if p.groupAllowlist == nil {
		return "", true
	}
	rc, ok := replyCtx.(wsReplyContext)
	if !ok {
		return "", false
	}
	if rc.chatType == "single" {
		return "", true
	}
	if rc.chatType != "group" || rc.chatID == "" {
		return "", false
	}
	p.groupAllowlist.mu.RLock()
	defer p.groupAllowlist.mu.RUnlock()
	return rc.chatID, p.groupAllowlist.groups[rc.chatID]
}

func (p *WSPlatform) SetGroupAccess(groupID string, allowed bool) error {
	store := p.groupAllowlist
	if store == nil || strings.TrimSpace(groupID) == "" {
		return fmt.Errorf("wecom-ws: group allowlist is disabled or group ID is empty")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	updated := make(map[string]bool, len(store.groups)+1)
	for id := range store.groups {
		updated[id] = true
	}
	if allowed {
		updated[groupID] = true
	} else {
		delete(updated, groupID)
	}
	groups := make([]string, 0, len(updated))
	for id := range updated {
		groups = append(groups, id)
	}
	sort.Strings(groups)
	data, err := json.MarshalIndent(struct {
		Groups []string `json:"groups"`
	}{groups}, "", "  ")
	if err != nil {
		return fmt.Errorf("wecom-ws: encode group allowlist: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return fmt.Errorf("wecom-ws: create group allowlist directory: %w", err)
	}
	if err := core.AtomicWriteFile(store.path, data, 0o600); err != nil {
		return fmt.Errorf("wecom-ws: save group allowlist: %w", err)
	}
	store.groups = updated
	return nil
}
