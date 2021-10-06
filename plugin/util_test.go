package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_AZDistribution(t *testing.T) {
	testCases := []struct {
		name         string
		azList       []string
		azDist       map[string]int
		count        int
		expectedDist map[string]int
	}{
		{
			name:         "simple distribution",
			azList:       []string{"AZ1", "AZ2", "AZ3"},
			azDist:       map[string]int{"AZ1": 5, "AZ2": 1, "AZ3": 3},
			count:        5,
			expectedDist: map[string]int{"AZ1": 5, "AZ2": 5, "AZ3": 4},
		},
		{
			name:         "az removed from list",
			azList:       []string{"AZ1"},
			azDist:       map[string]int{"AZ1": 5, "AZ2": 1},
			count:        3,
			expectedDist: map[string]int{"AZ1": 8, "AZ2": 1},
		},
		{
			name:         "az changed",
			azList:       []string{"AZ1"},
			azDist:       map[string]int{"AZ2": 5},
			count:        3,
			expectedDist: map[string]int{"AZ1": 3, "AZ2": 5},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ccd := make([]*customCreateData, tc.count)
			for i := range ccd {
				ccd[i] = &customCreateData{name: "test"}
			}
			distributeAZ(tc.azList, tc.azDist, ccd)

			result := make(map[string]int)
			for _, v := range ccd {
				result[v.availabilityzone] = result[v.availabilityzone] + 1
			}
			for az, c := range tc.azDist {
				result[az] = result[az] + c
			}

			assert.Equal(t, tc.expectedDist, result, tc.name)
		})
	}
}
