package embedding

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"research-scout/internal/config"
)

const Stage0Version = "stage0-deterministic-embedding-v1"

type Stub struct {
	Dim int
}

func NewStub(cfg config.Config) Stub {
	dim := cfg.EmbeddingDim
	if dim <= 0 {
		dim = 1024
	}
	return Stub{Dim: dim}
}

// Vector returns a deterministic, normalized lexical hash embedding. It is a
// test fixture provider, not a semantic model, and never performs network I/O.
func (s Stub) Vector(text string) []float32 {
	dim := s.Dim
	if dim <= 0 {
		dim = 1024
	}
	out := make([]float32, dim)
	for _, token := range strings.Fields(strings.ToLower(text)) {
		sum := sha256.Sum256([]byte(token))
		idx := binary.LittleEndian.Uint64(sum[:8]) % uint64(dim)
		sign := float32(1)
		if sum[8]&1 == 1 {
			sign = -1
		}
		out[idx] += sign * (1 + float32(len(token)%7))
	}
	var norm float64
	for _, v := range out {
		norm += float64(v * v)
	}
	if norm == 0 {
		out[0] = 1
		return out
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range out {
		out[i] *= scale
	}
	return out
}

func FormatVector(v []float32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = fmt.Sprintf("%.8f", x)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
