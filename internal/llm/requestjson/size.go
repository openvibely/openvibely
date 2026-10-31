package requestjson

import "github.com/openvibely/openvibely/internal/llm/jsonview"

func StringSize(s string) int                   { return jsonview.StringSize(s) }
func IntegerSize(v int64) int                   { return jsonview.IntegerSize(v) }
func UnsignedIntegerSize(v uint64) int          { return jsonview.UnsignedIntegerSize(v) }
func FloatSize(v float64, bits int) (int, bool) { return jsonview.FloatSize(v, bits) }
func Size(v any) (int, error)                   { return jsonview.Size(v) }
