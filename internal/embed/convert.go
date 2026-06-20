package embed

import "fmt"

func float64sToFloat32s(raw []float64) []float32 {
	embedding := make([]float32, len(raw))
	for i, value := range raw {
		embedding[i] = float32(value)
	}
	return embedding
}

func validateEmbeddingDimensions(provider string, embedding []float32, dimensions int) error {
	if dimensions <= 0 || len(embedding) == dimensions {
		return nil
	}
	return NewProviderError(provider, "embed", fmt.Errorf("%w: expected %d dimensions, got %d", ErrDimensionMismatch, dimensions, len(embedding)))
}
