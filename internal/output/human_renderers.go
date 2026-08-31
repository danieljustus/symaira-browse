package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// writeHumanValue renders the common daemon payloads for people and falls back
// to deterministic indented JSON for payloads without a dedicated renderer.
// JSON and YAML callers never reach this path.
func writeHumanValue(w io.Writer, data any) error {
	if rendered, ok := renderHumanPayload(data); ok {
		_, err := io.WriteString(w, rendered)
		return err
	}

	raw, err := json.MarshalIndent(data, "", "  ")
	if err == nil {
		raw = append(raw, '\n')
		_, err = w.Write(raw)
		return err
	}
	_, err = fmt.Fprintln(w, data)
	return err
}

func renderHumanPayload(data any) (string, bool) {
	fields, ok := humanFields(data)
	if !ok {
		return "", false
	}
	if rendered, ok := renderSnapshotPayload(fields); ok {
		return rendered, true
	}
	if rendered, ok := renderNavigationPayload(fields); ok {
		return rendered, true
	}
	if rendered, ok := renderStatePayload(fields); ok {
		return rendered, true
	}
	if rendered, ok := renderCookiesPayload(fields); ok {
		return rendered, true
	}
	if rendered, ok := renderTabsPayload(fields); ok {
		return rendered, true
	}
	return "", false
}

func humanFields(data any) (map[string]any, bool) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, false
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

func renderNavigationPayload(fields map[string]any) (string, bool) {
	action, actionOK := stringField(fields, "action")
	url, urlOK := stringField(fields, "url")
	if !actionOK || !urlOK || action == "" || url == "" {
		return "", false
	}
	line := fmt.Sprintf("%s %s", action, url)
	if status, ok := integerField(fields, "http_status"); ok && status > 0 {
		line += fmt.Sprintf(" (HTTP %d)", status)
	}
	return line + "\n", true
}

func renderSnapshotPayload(fields map[string]any) (string, bool) {
	if hasAny(fields, "added", "removed", "changed") {
		return renderSnapshotDiff(fields), true
	}
	tree, treeOK := stringField(fields, "tree")
	_, refsOK := fields["refs"]
	_, idOK := fields["snapshot_id"]
	if !treeOK || (!refsOK && !idOK) {
		return "", false
	}
	heading := "tree:"
	if id, ok := stringField(fields, "snapshot_id"); ok && id != "" {
		heading = id + " tree:"
	}
	var builder strings.Builder
	builder.WriteString(heading)
	builder.WriteByte('\n')
	if trimmed := strings.TrimRight(tree, "\n"); trimmed != "" {
		builder.WriteString(trimmed)
		builder.WriteByte('\n')
	}
	if hint, ok := stringField(fields, "hint"); ok && hint != "" {
		builder.WriteString("hint: ")
		builder.WriteString(hint)
		builder.WriteByte('\n')
	}
	return builder.String(), true
}

func renderSnapshotDiff(fields map[string]any) string {
	heading := "snapshot diff:"
	if id, ok := stringField(fields, "snapshot_id"); ok && id != "" {
		heading = id + " diff:"
	}
	var builder strings.Builder
	builder.WriteString(heading)
	builder.WriteByte('\n')
	for _, section := range []struct {
		key   string
		label string
	}{
		{key: "added", label: "added"},
		{key: "removed", label: "removed"},
		{key: "changed", label: "changed"},
	} {
		items, ok := fields[section.key].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		builder.WriteString(section.label)
		builder.WriteString(":\n")
		for _, item := range items {
			builder.WriteString("- ")
			builder.WriteString(snapshotItemText(item))
			builder.WriteByte('\n')
		}
	}
	if hint, ok := stringField(fields, "hint"); ok && hint != "" {
		builder.WriteString("hint: ")
		builder.WriteString(hint)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func snapshotItemText(item any) string {
	fields, ok := humanFields(item)
	if !ok {
		return fmt.Sprint(item)
	}
	if ref, ok := stringField(fields, "ref"); ok && ref != "" {
		return ref
	}
	if ref, ok := stringField(fields, "refkey"); ok && ref != "" {
		return ref
	}
	role, _ := stringField(fields, "role")
	name, _ := stringField(fields, "name")
	if role == "" {
		role = "node"
	}
	if name != "" {
		return fmt.Sprintf("%s %q", role, name)
	}
	return role
}

func renderStatePayload(fields map[string]any) (string, bool) {
	if saved, ok := stringField(fields, "saved"); ok {
		return renderStateOperation("saved", saved, fields), true
	}
	if loaded, ok := stringField(fields, "loaded"); ok {
		return renderStateOperation("loaded", loaded, fields), true
	}
	if cleared, ok := stringField(fields, "cleared"); ok {
		return fmt.Sprintf("cleared: %s\n", cleared), true
	}
	if removed, ok := stringListField(fields, "removed"); ok {
		return renderList("removed", removed), true
	}
	if states, ok := stringListField(fields, "states"); ok {
		return renderList("states", states), true
	}
	if metadata, ok := mapField(fields, "metadata"); ok {
		return renderStateMetadata(metadata, "metadata:"), true
	}
	if hasAny(fields, "schema_version", "saved_at", "expires_at", "key_source") && hasKey(fields, "origins") {
		return renderStateMetadata(fields, ""), true
	}
	return "", false
}

func renderStateOperation(operation, name string, fields map[string]any) string {
	var builder strings.Builder
	builder.WriteString(operation)
	builder.WriteString(": ")
	builder.WriteString(name)
	builder.WriteByte('\n')
	if metadata, ok := mapField(fields, "metadata"); ok {
		builder.WriteString(renderStateMetadata(metadata, "metadata:"))
	}
	return builder.String()
}

func renderStateMetadata(fields map[string]any, heading string) string {
	var builder strings.Builder
	if heading != "" {
		builder.WriteString(heading)
		builder.WriteByte('\n')
	}
	for _, field := range []struct {
		key   string
		label string
	}{
		{key: "name", label: "name"},
		{key: "schema_version", label: "schema version"},
		{key: "saved_at", label: "saved at"},
		{key: "expires_at", label: "expires at"},
		{key: "key_source", label: "key source"},
	} {
		if value, ok := fields[field.key]; ok {
			builder.WriteString(field.label)
			builder.WriteString(": ")
			builder.WriteString(humanScalar(value))
			builder.WriteByte('\n')
		}
	}
	if origins, ok := fields["origins"].([]any); ok {
		builder.WriteString("origins:\n")
		for _, origin := range origins {
			builder.WriteString("- ")
			builder.WriteString(originMetadataText(origin))
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func originMetadataText(origin any) string {
	fields, ok := humanFields(origin)
	if !ok {
		return fmt.Sprint(origin)
	}
	name, _ := stringField(fields, "origin")
	cookies, _ := integerField(fields, "cookie_count")
	local, _ := integerField(fields, "local_storage_keys")
	session, _ := integerField(fields, "session_storage_keys")
	return fmt.Sprintf("%s (cookies=%d, local=%d, session=%d)", name, cookies, local, session)
}

func renderCookiesPayload(fields map[string]any) (string, bool) {
	cookies, ok := fields["cookies"].([]any)
	if !ok {
		return "", false
	}
	var builder strings.Builder
	if origin, ok := stringField(fields, "origin"); ok && origin != "" {
		builder.WriteString("origin: ")
		builder.WriteString(origin)
		builder.WriteByte('\n')
	}
	builder.WriteString("cookies:\n")
	for _, cookie := range cookies {
		builder.WriteString("- ")
		builder.WriteString(cookieText(cookie))
		builder.WriteByte('\n')
	}
	return builder.String(), true
}

func cookieText(cookie any) string {
	fields, ok := humanFields(cookie)
	if !ok {
		return fmt.Sprint(cookie)
	}
	name, _ := stringField(fields, "name")
	if name == "" {
		name = "cookie"
	}
	parts := []string{name}
	for _, field := range []struct {
		key   string
		label string
	}{
		{key: "domain", label: "domain"},
		{key: "path", label: "path"},
		{key: "same_site", label: "same_site"},
	} {
		if value, ok := stringField(fields, field.key); ok && value != "" {
			parts = append(parts, field.label+"="+value)
		}
	}
	if flag, ok := fields["secure"].(bool); ok && flag {
		parts = append(parts, "secure")
	}
	if flag, ok := fields["http_only"].(bool); ok && flag {
		parts = append(parts, "httpOnly")
	}
	if flag, ok := fields["session"].(bool); ok && flag {
		parts = append(parts, "session")
	}
	return strings.Join(parts, " ")
}

func renderTabsPayload(fields map[string]any) (string, bool) {
	tabs, ok := fields["tabs"].([]any)
	if !ok {
		return "", false
	}
	var builder strings.Builder
	if active, ok := stringField(fields, "active"); ok && active != "" {
		builder.WriteString("active: ")
		builder.WriteString(active)
		builder.WriteByte('\n')
	}
	builder.WriteString("tabs:\n")
	for _, tab := range tabs {
		builder.WriteString("- ")
		builder.WriteString(tabText(tab))
		builder.WriteByte('\n')
	}
	return builder.String(), true
}

func tabText(tab any) string {
	fields, ok := humanFields(tab)
	if !ok {
		return fmt.Sprint(tab)
	}
	id, _ := stringField(fields, "id")
	label, _ := stringField(fields, "label")
	url, _ := stringField(fields, "url")
	parts := make([]string, 0, 4)
	if id != "" {
		parts = append(parts, id)
	}
	if label != "" && label != id {
		parts = append(parts, strconv.Quote(label))
	}
	if url != "" {
		parts = append(parts, url)
	}
	if active, ok := fields["active"].(bool); ok && active {
		parts = append(parts, "[active]")
	}
	if len(parts) == 0 {
		return "tab"
	}
	return strings.Join(parts, " ")
}

func renderList(label string, values []string) string {
	var builder strings.Builder
	builder.WriteString(label)
	builder.WriteString(":\n")
	for _, value := range values {
		builder.WriteString("- ")
		builder.WriteString(value)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func stringListField(fields map[string]any, key string) ([]string, bool) {
	value, ok := fields[key]
	if !ok {
		return nil, false
	}
	switch values := value.(type) {
	case []string:
		return values, true
	case []any:
		result := make([]string, 0, len(values))
		for _, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			result = append(result, text)
		}
		return result, true
	default:
		return nil, false
	}
}

func mapField(fields map[string]any, key string) (map[string]any, bool) {
	value, ok := fields[key]
	if !ok {
		return nil, false
	}
	return humanFields(value)
}

func stringField(fields map[string]any, key string) (string, bool) {
	value, ok := fields[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func integerField(fields map[string]any, key string) (int64, bool) {
	value, ok := fields[key]
	if !ok {
		return 0, false
	}
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int64:
		return number, true
	case float64:
		return int64(number), number == float64(int64(number))
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func humanScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func hasKey(fields map[string]any, key string) bool {
	_, ok := fields[key]
	return ok
}

func hasAny(fields map[string]any, keys ...string) bool {
	for _, key := range keys {
		if hasKey(fields, key) {
			return true
		}
	}
	return false
}
