package controller

import (
	"cmp"
	"slices"
)

const agentBaseCPUMillicores = int64(500)

type cpuDemand struct {
	key   string
	limit int64
}

// maxMinCPU distributes pool millicores with demand caps. Remainders are
// assigned by stable key order so repeated plans produce the same result.
func maxMinCPU(pool int64, demands []cpuDemand) map[string]int64 {
	result := make(map[string]int64, len(demands))
	active := append([]cpuDemand(nil), demands...)
	slices.SortFunc(active, func(left, right cpuDemand) int {
		return cmp.Compare(left.key, right.key)
	})
	for pool > 0 && len(active) > 0 {
		share := pool / int64(len(active))
		remainder := pool % int64(len(active))
		used := int64(0)
		next := active[:0]
		for i := range active {
			grant := share
			if int64(i) < remainder {
				grant++
			}
			remaining := active[i].limit - result[active[i].key]
			grant = min(grant, remaining)
			if grant > 0 {
				result[active[i].key] += grant
				used += grant
			}
			if result[active[i].key] < active[i].limit {
				next = append(next, active[i])
			}
		}
		if used == 0 {
			break
		}
		pool -= used
		active = next
	}
	return result
}

type proportionalShare struct {
	key       string
	capacity  int64
	allocated int64
	remainder int64
}

// proportionalCPU returns a deterministic proportional allocation whose sum
// is min(amount, total capacity).
func proportionalCPU(amount int64, capacities map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(capacities))
	total := int64(0)
	for _, capacity := range capacities {
		if capacity > 0 {
			total += capacity
		}
	}
	amount = min(max(amount, 0), total)
	if amount == 0 || total == 0 {
		return result
	}
	shares := make([]proportionalShare, 0, len(capacities))
	used := int64(0)
	for key, capacity := range capacities {
		if capacity <= 0 {
			continue
		}
		numerator := amount * capacity
		allocated := numerator / total
		shares = append(shares, proportionalShare{
			key: key, capacity: capacity, allocated: allocated,
			remainder: numerator % total,
		})
		result[key] = allocated
		used += allocated
	}
	slices.SortFunc(shares, func(left, right proportionalShare) int {
		if order := cmp.Compare(right.remainder, left.remainder); order != 0 {
			return order
		}
		return cmp.Compare(left.key, right.key)
	})
	for remaining := amount - used; remaining > 0; remaining-- {
		for i := range shares {
			if result[shares[i].key] < shares[i].capacity {
				result[shares[i].key]++
				break
			}
		}
	}
	return result
}
