package platform

import "github.com/NARUBROWN/spine"

// Option connects the fasthttp factory to a Spine application.
func Option(options ...FastHTTPOption) spine.Option {
	return spine.WithHTTPAdapter(NewFactory(options...))
}

// New creates a Spine application with the fasthttp platform.
func New(options ...FastHTTPOption) spine.App { return spine.New(Option(options...)) }
