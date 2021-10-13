package plugin

import (
	"bytes"
	crand "crypto/rand"
	"fmt"
	"sort"
	"text/template"

	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
)

const (
	flavorCacheKey  = "flavor:%s"
	imageCacheKey   = "image:%s"
	networkCacheKey = "network:%s"
)

type azInstanceDist struct {
	AZName string
	Count  int
}

func distributeAZ(azList []string, azDist map[string]int, ccd []*customCreateData) {
	azID := make([]azInstanceDist, len(azList))
	for i, az := range azList {
		count := azDist[az]
		azID[i] = azInstanceDist{AZName: az, Count: count}
	}
	sort.SliceStable(azID, func(i, j int) bool {
		return azID[i].Count < azID[j].Count
	})

	if len(azID) == 0 {
		return
	}

	currentIndex := 0
	for _, createData := range ccd {
		createData.availabilityzone = azID[currentIndex].AZName
		azID[currentIndex].Count += 1
		// if we're in the last member, we reached the AZ with most instances, we should be back at the first
		if currentIndex+1 == len(azID) {
			currentIndex = 0
			continue
		}
		// if current has more than the following we advance the index
		if azID[currentIndex].Count > azID[currentIndex+1].Count {
			currentIndex += 1
			// if the current is not the first one and reached it's value, we came back to the first position
		} else if currentIndex != 0 && azID[currentIndex].Count >= azID[0].Count {
			currentIndex = 0
		}
	}
}

type templateData struct {
	Name            string
	AZ              string
	RandomUUID      string
	ShortRandomUUID string
	PoolName        string
}

func dataToCreateOpts(common *commonCreateData, custom *customCreateData) (servers.CreateOpts, error) {
	opts := servers.CreateOpts{
		Name:           common.name,
		ImageRef:       common.imageID,
		FlavorRef:      common.flavorID,
		SecurityGroups: common.securityGroups,
		Metadata:       common.metadata,
		Tags:           common.tags,
	}
	if common.networkUUID != "" {
		opts.Networks = []servers.Network{{UUID: common.networkUUID}}
	}

	if custom.name != "" {
		opts.Name = custom.name
	}
	if custom.availabilityzone != "" {
		opts.AvailabilityZone = custom.availabilityzone
	}

	opts.Tags = append(opts.Tags, fmt.Sprintf(poolTag, common.pool))

	if common.userDataTemplate != "" {
		template, err := template.ParseFiles(common.userDataTemplate)
		if err != nil {
			return opts, fmt.Errorf("error parsing template file %s: %s", common.userDataTemplate, err)
		}

		td := templateData{
			Name:       custom.name,
			AZ:         custom.availabilityzone,
			RandomUUID: custom.randomUUID,
			PoolName:   common.pool,
		}
		if td.RandomUUID != "" {
			td.ShortRandomUUID = td.RandomUUID[0:13]
		}

		buf := new(bytes.Buffer)
		if err := template.Execute(buf, td); err != nil {
			return opts, fmt.Errorf("error executing template file %s: %s", common.userDataTemplate, err)
		}
		opts.UserData = buf.Bytes()
	}

	return opts, nil
}

func generateUUID() string {
	buf := make([]byte, 16)
	if _, err := crand.Read(buf); err != nil {
		panic(fmt.Errorf("failed to read random bytes: %v", err))
	}

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%12x",
		buf[0:4],
		buf[4:6],
		buf[6:8],
		buf[8:10],
		buf[10:16])
}

func cachekey(format, name string) string {
	return fmt.Sprintf(format, name)
}
