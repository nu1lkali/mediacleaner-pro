package media

import (
	"encoding/binary"
	"math"
)

func mathFloat32(b []byte) float32 { return math.Float32frombits(binary.BigEndian.Uint32(b)) }
func mathFloat64(b []byte) float64 { return math.Float64frombits(binary.BigEndian.Uint64(b)) }
