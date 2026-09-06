package api

import "fmt"

// LayerValidationError identifies malformed configuration before composition.
type LayerValidationError struct {
	Layer string
	Err   error
}

func (e *LayerValidationError) Error() string {
	return fmt.Sprintf("spec layer %q: %v", e.Layer, e.Err)
}

func (e *LayerValidationError) Unwrap() error { return e.Err }

// ValidateSpecLayers checks authored structure without requiring a model or prompt.
func ValidateSpecLayers(layers ...SpecLayer) error {
	for _, layer := range layers {
		if err := validateSpecLayer(layer); err != nil {
			return &LayerValidationError{Layer: layer.Name, Err: err}
		}
		if err := layer.Spec.ValidateStructure(); err != nil {
			return &LayerValidationError{Layer: layer.Name, Err: err}
		}
	}
	return nil
}
