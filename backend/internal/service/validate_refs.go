package service

import (
	"context"

	"configcenter/internal/domain"
	"configcenter/internal/merge"
	"configcenter/internal/ref"
	"configcenter/internal/validator"
)

// validateResolvedShape dereferences a hypothetical (not yet committed) value
// against the latest committed targets and validates the resulting document,
// including its JSON Schema. This is what makes values such as
//
//	{"timeout": @{base_timeout}}
//
// legal to author: the raw text is not parseable JSON, but the document is
// valid after substitution. A target that has no value in this environment
// cannot be expanded yet; shape validation is skipped for it and the read
// path keeps reporting ref_no_value loudly.
func (s *Service) validateResolvedShape(ctx context.Context, item *domain.Item, env, value string) error {
	r := newResolver(s, ctx, item.TenantID, env, InstanceContext{})
	r.latestOnly = true

	from := nodeCoord{ns: item.NamespaceID, grp: item.GroupID, key: item.Key, env: env}
	input := merge.LayerInput{Layer: item.Layer, Format: item.Format, Value: value}
	node, err := r.resolveDocument(item.Format, []merge.LayerInput{input}, item, from,
		"hypothetical:"+item.ID+"#"+env, 0)
	if err != nil {
		if re, ok := err.(*RefError); ok && re.Code == CodeRefNoValue {
			return nil // read-time error; cannot expand yet
		}
		return err
	}
	if out := validator.Validate(item.Format, node.entry.Value, item.Schema); !out.Valid {
		return &ValidationFailure{Errors: out.Errors}
	}
	return nil
}

// valueHasPlaceholder is a small convenience for the commit paths.
func valueHasPlaceholder(value string) bool { return ref.HasPlaceholder(value) }

var _ = domain.FormatJSON
