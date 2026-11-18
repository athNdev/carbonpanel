// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	db "github.com/athNdev/carbon-panel/internal/db"
)

const (
	maxStatFields     = 40
	maxCollapsedStats = 16
	maxAdvancements   = 512
	maxAdvFields      = 16
	maxAdvCollapsed   = 16
)

// parseStats reads {"DataVersion":n,"stats":{...}} defensively. Unknown keys
// are never dropped silently: they are collapsed, sorted and bounded into a
// handful of summary fields so new Minecraft stats remain visible without
// letting a hostile file inflate the result.
func parseStats(data []byte, server *db.Server) []Field {
	v, err := decodeJSON(data)
	if err != nil {
		return []Field{unknownField("stats", SourceOnDiskJSON,
			fmt.Sprintf("malformed or truncated stats JSON: %v", err), server)}
	}
	root, ok := v.(map[string]any)
	if !ok {
		return []Field{unknownField("stats", SourceOnDiskJSON, "stats JSON is not an object", server)}
	}
	now := time.Now().UTC()
	conf := diskConfidence(server)
	fields := make([]Field, 0, maxStatFields)

	if dv, ok := numInt(root["DataVersion"]); ok {
		fields = append(fields, Field{
			Name: "data_version", Value: strconv.FormatInt(dv, 10),
			Source: SourceOnDiskJSON, ObservedAt: now, Confidence: conf,
		})
	}

	stats, ok := root["stats"].(map[string]any)
	if !ok {
		// Some tools flatten stats into the top level; accept that as well.
		stats = root
	}

	buckets := map[string][]string{}
	counts := map[string]int{}
	seen := map[string]bool{}
	for category, raw := range stats {
		entries, ok := raw.(map[string]any)
		if !ok {
			// Flattened form: the "category" is actually a leaf key.
			entries = map[string]any{category: raw}
		}
		for key, val := range entries {
			n, ok := numInt(val)
			if !ok {
				continue
			}
			full := key
			if _, nested := raw.(map[string]any); nested {
				full = category + ":" + key
			}
			if seen[full] {
				continue
			}
			seen[full] = true
			if name, value, unit, bucket, ok := classifyStat(full, n); ok {
				if bucket != "" {
					buckets[bucket] = append(buckets[bucket], shortKey(key)+"="+value)
					counts[bucket]++
				} else {
					fields = append(fields, Field{
						Name: name, Value: value, Unit: unit,
						Source: SourceOnDiskJSON, ObservedAt: now, Confidence: conf,
					})
					if len(fields) >= maxStatFields {
						break
					}
				}
			} else if bucket := rawBucket(full); bucket != "" {
				buckets[bucket] = append(buckets[bucket], shortKey(key)+"="+strconv.FormatInt(n, 10))
				counts[bucket]++
			} else {
				buckets["other_stats"] = append(buckets["other_stats"], shortKey(full)+"="+strconv.FormatInt(n, 10))
				counts["other_stats"]++
			}
		}
		if len(fields) >= maxStatFields {
			break
		}
	}

	for _, name := range []string{"item_stats", "block_stats", "entity_stats", "other_stats"} {
		values := buckets[name]
		if len(values) == 0 {
			continue
		}
		sort.Strings(values)
		value, note := collapse(values, maxCollapsedStats)
		if note != "" {
			note = fmt.Sprintf("%d additional %s keys omitted for bounds", counts[name]-maxCollapsedStats, name)
		}
		fields = append(fields, Field{
			Name: name, Value: value, Source: SourceOnDiskJSON,
			ObservedAt: now, Confidence: conf, ConflictNote: note,
		})
	}
	if len(fields) == 0 {
		fields = append(fields, unknownField("stats", SourceOnDiskJSON,
			"stats object contained no recognized numeric values", server))
	}
	return fields
}

// classifyStat maps the well-known statistics. The combined key is either
// "minecraft:custom:minecraft:play_time" (nested) or
// "minecraft.custom:minecraft.play_time" (flattened); suffix matching handles
// both spellings. Values are described in the Minecraft wiki: play_time is in
// ticks (20 ticks/second), distances are in centimetres.
func classifyStat(full string, n int64) (name, value, unit, bucket string, ok bool) {
	switch {
	case strings.HasSuffix(full, "play_time"):
		return "play_time", (time.Duration(n/20) * time.Second).String(), "", "", true
	case strings.HasSuffix(full, "deaths"):
		return "deaths", strconv.FormatInt(n, 10), "count", "", true
	case strings.HasSuffix(full, "jump"):
		return "jump", strconv.FormatInt(n, 10), "count", "", true
	}
	for _, d := range []string{
		"walk_distance", "sprint_distance", "fly_distance", "swim_distance",
		"boat_distance", "horse_distance", "minecart_distance", "aviate_distance",
		"climb_distance", "crouch_distance", "walk_on_water_distance",
		"walk_under_water_distance", "fall_distance",
	} {
		if strings.HasSuffix(full, d) {
			meters := float64(n) / 100
			return d, strconv.FormatFloat(meters, 'f', 2, 64), "m", "", true
		}
	}
	return "", "", "", "", false
}

func rawBucket(full string) string {
	switch {
	case strings.HasPrefix(full, "minecraft:item:") || strings.HasPrefix(full, "minecraft.item:"):
		return "item_stats"
	case strings.HasPrefix(full, "minecraft:block:") || strings.HasPrefix(full, "minecraft.block:"):
		return "block_stats"
	case strings.HasPrefix(full, "minecraft:entity:") || strings.HasPrefix(full, "minecraft.entity:"):
		return "entity_stats"
	}
	return ""
}

func shortKey(key string) string {
	if i := strings.LastIndexAny(key, ":."); i >= 0 && i+1 < len(key) {
		return key[i+1:]
	}
	return key
}

func collapse(values []string, max int) (string, string) {
	if len(values) > max {
		extra := len(values) - max
		return strings.Join(values[:max], ", ") + fmt.Sprintf(" (+%d more)", extra),
			fmt.Sprintf("%d additional keys omitted for bounds", extra)
	}
	return strings.Join(values, ", "), ""
}

// parseAdvancements reads the per-advancement done/criteria/timestamp objects
// and bounds the number of emitted fields. A malformed file yields one
// "unknown" field rather than failing the whole RPC.
func parseAdvancements(data []byte, server *db.Server) []Field {
	v, err := decodeJSON(data)
	if err != nil {
		return []Field{unknownField("advancements", SourceOnDiskJSON,
			fmt.Sprintf("malformed or truncated advancements JSON: %v", err), server)}
	}
	root, ok := v.(map[string]any)
	if !ok {
		return []Field{unknownField("advancements", SourceOnDiskJSON, "advancements JSON is not an object", server)}
	}
	now := time.Now().UTC()
	conf := diskConfidence(server)
	ids := make([]string, 0, len(root))
	for id := range root {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fields := make([]Field, 0, maxAdvFields+2)
	doneCount := 0
	processed := 0
	omitted := 0
	for _, id := range ids {
		if processed >= maxAdvancements {
			omitted++
			continue
		}
		processed++
		obj, ok := root[id].(map[string]any)
		if !ok {
			continue
		}
		done, _ := obj["done"].(bool)
		criteria, _ := obj["criteria"].(map[string]any)
		achieved, total := countCriteria(criteria)
		stamp := firstTimestamp(criteria)
		if done {
			doneCount++
		}
		if len(fields) < maxAdvFields {
			value := "in_progress"
			if done {
				value = "done"
				if !stamp.IsZero() {
					value += " at " + stamp.UTC().Format(time.RFC3339)
				}
			} else if total > 0 {
				value = fmt.Sprintf("in_progress (%d/%d criteria)", achieved, total)
			}
			fields = append(fields, Field{
				Name: "advancement_" + id, Value: value,
				Source: SourceOnDiskJSON, ObservedAt: now, Confidence: conf,
			})
		} else {
			omitted++
		}
	}
	summary := Field{
		Name:   "advancements_summary",
		Value:  fmt.Sprintf("%d completed / %d recorded", doneCount, processed),
		Source: SourceOnDiskJSON, ObservedAt: now, Confidence: conf,
	}
	if omitted > 0 {
		summary.ConflictNote = fmt.Sprintf("%d advancement keys omitted for bounds", omitted)
	}
	fields = append(fields, summary)
	return fields
}

// countCriteria counts achieved criteria. Minecraft stores criteria values as
// either a single [year,month,day,hour,min,sec] array or an array of such
// arrays.
func countCriteria(criteria map[string]any) (achieved, total int) {
	total = len(criteria)
	for _, raw := range criteria {
		if _, ok := criterionTimestamp(raw); ok {
			achieved++
		}
	}
	return
}

// firstTimestamp returns the earliest recorded completion timestamp.
func firstTimestamp(criteria map[string]any) time.Time {
	var earliest time.Time
	for _, raw := range criteria {
		if ts, ok := criterionTimestamp(raw); ok && (earliest.IsZero() || ts.Before(earliest)) {
			earliest = ts
		}
	}
	return earliest
}

func criterionTimestamp(raw any) (time.Time, bool) {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return time.Time{}, false
	}
	if nested, ok := arr[0].([]any); ok {
		if len(nested) == 0 {
			return time.Time{}, false
		}
		return parseMinecraftStamp(nested)
	}
	return parseMinecraftStamp(arr)
}

func parseMinecraftStamp(arr []any) (time.Time, bool) {
	if len(arr) < 3 {
		return time.Time{}, false
	}
	year, ok1 := numInt(arr[0])
	month, ok2 := numInt(arr[1])
	day, ok3 := numInt(arr[2])
	if !ok1 || !ok2 || !ok3 || year < 1 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	hour, minute, second := int64(0), int64(0), int64(0)
	if len(arr) > 3 {
		hour, _ = numInt(arr[3])
	}
	if len(arr) > 4 {
		minute, _ = numInt(arr[4])
	}
	if len(arr) > 5 {
		second, _ = numInt(arr[5])
	}
	return time.Date(int(year), time.Month(month), int(day), int(hour), int(minute), int(second), 0, time.UTC), true
}

func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func numInt(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		if f, err := n.Float64(); err == nil {
			return int64(f), true
		}
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}
