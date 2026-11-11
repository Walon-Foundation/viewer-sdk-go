package viewer

import "sort"

// sortFields orders map entries by key, so the same map always encodes the same way.
func sortFields(f []field) { sort.Slice(f, func(i, j int) bool { return f[i].Key < f[j].Key }) }
