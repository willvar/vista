package provider

import "reflect"

func testBool(v bool) *bool    { return &v }
func testInt64(v int64) *int64 { return &v }
func equalUsage(got *ModelUsage, want ModelUsage) bool {
	return got != nil && reflect.DeepEqual(*got, want)
}
