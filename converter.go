package slogloki

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/prometheus/common/model"
	slogcommon "github.com/samber/slog-common"
)

var SourceKey = "source"
var ErrorKeys = []string{"error", "err"}

// See:
//   - https://github.com/samber/slog-loki/issues/10
//   - https://github.com/samber/slog-loki/issues/11
var SubAttributeSeparator = "__"
var AttributeKeyInvalidCharReplacement = "_"

type Converter func(addSource bool, replaceAttr func(groups []string, a slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record) model.LabelSet

func DefaultConverter(addSource bool, replaceAttr func(groups []string, a slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record) model.LabelSet {
	// aggregate all attributes
	attrs := slogcommon.AppendRecordAttrsToAttrs(loggerAttr, groups, record)

	// developer formatters
	attrs = slogcommon.ReplaceError(attrs, ErrorKeys...)
	if addSource {
		attrs = append(attrs, slogcommon.Source(SourceKey, record))
	}
	attrs = append(attrs, slog.String("level", record.Level.String()))
	attrs = slogcommon.ReplaceAttrs(replaceAttr, []string{}, attrs...)
	attrs = slogcommon.RemoveEmptyAttrs(attrs)

	// handler formatter
	output := slogcommon.AttrsToMap(attrs...)

	labelSet := model.LabelSet{}
	flatten("", output, labelSet)

	return labelSet
}

// https://stackoverflow.com/questions/64419565/how-to-efficiently-flatten-a-map
func flatten(prefix string, src map[string]any, dest model.LabelSet) {
	if len(prefix) > 0 {
		prefix += SubAttributeSeparator
	}
	for k, v := range src {
		switch child := v.(type) {
		case map[string]any:
			flatten(prefix+k, child, dest)
		case []any:
			for i := 0; i < len(child); i++ {
				dest[model.LabelName(stripIvalidChars(prefix+k+SubAttributeSeparator+strconv.Itoa(i)))] = model.LabelValue(fmt.Sprintf("%v", child[i]))
			}
		default:
			dest[model.LabelName(stripIvalidChars(prefix+k))] = model.LabelValue(fmt.Sprintf("%v", v))
		}
	}
}

func stripIvalidChars(s string) string {
	// it would be more performent with a proper caching strategy+LRU
	var result strings.Builder
	for i := 0; i < len(s); i++ {
		b := s[i]
		if ('a' <= b && b <= 'z') ||
			('A' <= b && b <= 'Z') ||
			('0' <= b && b <= '9') ||
			b == '_' || b == ':' {
			result.WriteByte(b)
		} else {
			result.WriteString(AttributeKeyInvalidCharReplacement)
		}
	}
	return result.String()
}

type DataConverterOpts struct {
	ErrorKeys        []string
	SourceKey        string
	DataKey          string
	ExemptDataPrefix string
	ExemptKeys       []string
	UseImplied       bool
}

type DataConverterOpt func(*DataConverterOpts)

func DefaultDataConverterOpts() *DataConverterOpts {
	return &DataConverterOpts{
		ErrorKeys:        ErrorKeys,
		SourceKey:        SourceKey,
		DataKey:          "data",
		ExemptDataPrefix: "",
		ExemptKeys:       []string{"level", "err", "error"},
		UseImplied:       false,
	}
}

func DataConverterErrorKeyOpt(keys ...string) DataConverterOpt {
	return func(opts *DataConverterOpts) {
		opts.ErrorKeys = keys
	}
}

func DataConverterSourceKeyOpt(key string) DataConverterOpt {
	return func(opts *DataConverterOpts) {
		opts.SourceKey = key
	}
}

func DataConverterDataKeyOpt(key string) DataConverterOpt {
	return func(opts *DataConverterOpts) {
		opts.DataKey = key
	}
}

func DataConverterUseImpliedOpt() DataConverterOpt {
	return func(opts *DataConverterOpts) {
		opts.UseImplied = true
	}
}

func DataConverterExemptDataPrefixOpt(prefix string) DataConverterOpt {
	return func(opts *DataConverterOpts) {
		opts.ExemptDataPrefix = prefix
	}
}

func DataConveterSetExemptKeys(keys ...string) DataConverterOpt {
	return func(opts *DataConverterOpts) {
		opts.ExemptKeys = keys
	}
}

func NewDataConverter(dataPrefix string, opts ...DataConverterOpt) Converter {
	o := DefaultDataConverterOpts()
	for _, opt := range opts {
		opt(o)
	}

	return func(addSource bool, replaceAttr func(groups []string, a slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record) model.LabelSet {
		// aggregate all attributes
		attrs := slogcommon.AppendRecordAttrsToAttrs(loggerAttr, groups, record)

		// developer formatters
		attrs = replaceError(attrs, o.ErrorKeys...)
		if addSource {
			attrs = append(attrs, slogcommon.Source(o.SourceKey, record))
		}
		attrs = append(attrs, slog.String("level", record.Level.String()))
		attrs = slogcommon.ReplaceAttrs(replaceAttr, []string{}, attrs...)
		attrs = slogcommon.RemoveEmptyAttrs(attrs)

		dataMap := getAttrsDataMap(attrs, dataPrefix, o.UseImplied, o.ExemptDataPrefix, o.ExemptKeys, false)
		if len(dataMap) > 0 {
			b, err := json.Marshal(dataMap)
			if err == nil {
				attrs = append(attrs, slog.String(o.DataKey, string(b)))
			}
		}
		attrs = removeDataAttrs(attrs, dataPrefix, o.ExemptDataPrefix, o.DataKey, o.ExemptKeys)

		// handler formatter
		output := slogcommon.AttrsToMap(attrs...)

		labelSet := model.LabelSet{}
		flatten("", output, labelSet)

		return labelSet
	}
}

func getAttrsDataMap(attrs []slog.Attr, dataPrefix string, useImplied bool, exemptDataPrefix string, exemptKeys []string, isGroup bool) map[string]any {
	dataMap := map[string]any{}
	for _, attr := range attrs {
		if !strings.HasPrefix(attr.Key, exemptDataPrefix) && !slices.Contains(exemptKeys, attr.Key) && (useImplied || strings.HasPrefix(attr.Key, dataPrefix) || isGroup) {
			if attr.Value.Kind() == slog.KindGroup {
				if isGroup {
					dataMap[attr.Key] = getAttrsDataMap(attr.Value.Resolve().Group(), attr.Key, useImplied, exemptDataPrefix, exemptKeys, true)
				} else {
					dataMap[attr.Key[len(dataPrefix):]] = getAttrsDataMap(attr.Value.Resolve().Group(), attr.Key, useImplied, exemptDataPrefix, exemptKeys, true)
				}
			} else {
				if isGroup {
					dataMap[attr.Key] = attr.Value.Resolve().Any()
				} else {
					dataMap[attr.Key[len(dataPrefix):]] = attr.Value.Resolve().Any()
				}
			}
		}
	}
	return dataMap
}

func removeDataAttrs(attrs []slog.Attr, dataPrefix string, exemptDataPrefix string, dataKey string, exemptKeys []string) []slog.Attr {
	safe := []slog.Attr{}
	for _, attr := range attrs {
		if strings.HasPrefix(attr.Key, exemptDataPrefix) {
			safe = append(safe, slog.Attr{
				Key:   attr.Key[len(exemptDataPrefix):],
				Value: attr.Value,
			})
		} else if attr.Key == dataKey || !strings.HasPrefix(attr.Key, dataPrefix) || slices.Contains(exemptKeys, attr.Key) {
			safe = append(safe, attr)
		}
	}
	return safe
}

func replaceError(attrs []slog.Attr, errorKeys ...string) []slog.Attr {
	replaceAttr := func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) > 1 {
			return a
		}

		for i := range errorKeys {
			if a.Key == errorKeys[i] {
				if err, ok := a.Value.Any().(error); ok {
					return slog.Any(a.Key, err.Error())
				}
			}
		}
		return a
	}
	return slogcommon.ReplaceAttrs(replaceAttr, []string{}, attrs...)
}
