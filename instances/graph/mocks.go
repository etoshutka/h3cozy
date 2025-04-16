// This file contains mock data for the instances/graph schema.
package graph

import (
	"gqlfed/instances/graph/model"
	"math/rand"
	"time"
)

var possibleStatuses = []string{"ACTIVE", "BUILDING", "STOPPED", "ERROR"}

func init() {
	rand.Seed(time.Now().UnixNano())
}

var mockFlavorList = []*model.BaseFlavor{
	{
		OriginalName: "standard-2-4",
		Vcpus:        "2",
		RAM:          "4",
		RubMonth:     "1000",
	},
	{
		OriginalName: "premium-4-8",
		Vcpus:        "4",
		RAM:          "8",
		RubMonth:     "2000",
	},
	{
		OriginalName: "standard-4-16",
		Vcpus:        "4",
		RAM:          "16",
		RubMonth:     "3000",
	},
	{
		OriginalName: "premium-8-32",
		Vcpus:        "8",
		RAM:          "32",
		RubMonth:     "5000",
	},
	{
		OriginalName: "extreme-16-64",
		Vcpus:        "16",
		RAM:          "64",
		RubMonth:     "8000",
	},
	{
		OriginalName: "storage-1-1",
		Vcpus:        "1",
		RAM:          "1",
		RubMonth:     "500",
	},
}

var Instances = []*model.Instance{
	{
		InstanceID:       "inst-001",
		ProjectID:        "proj-id-001",
		Name:             "test-server-1",
		Status:           "BUILDING",
		Created:          "2024-02-03T18:30:00Z",
		Updated:          time.Now().Format(time.RFC3339),
		KeyName:          "default-key",
		Flavor:           mockFlavorList[0],
		Locked:           false,
		PowerState:       "running",
		IPV4:             "192.168.1.100",
		AttachedDisks:    getRandomDisks(),
		AttachedNetworks: getRandomNetworks(),
		Loading:          rand.Intn(2) == 0,
	},
	{
		InstanceID:       "inst-002",
		ProjectID:        "proj-id-001",
		Name:             "test-server-2",
		Status:           "BUILDING",
		Created:          "2024-02-03T19:00:00Z",
		Updated:          time.Now().Format(time.RFC3339),
		KeyName:          "default-key",
		Flavor:           mockFlavorList[1],
		Locked:           false,
		PowerState:       "running",
		IPV4:             "192.168.1.101",
		AttachedDisks:    getRandomDisks(),
		AttachedNetworks: getRandomNetworks(),
		Loading:          rand.Intn(2) == 0,
	},
	{
		InstanceID:       "inst-003",
		ProjectID:        "proj-id-001",
		Name:             "test-server-3",
		Status:           "BUILDING",
		Created:          "2024-02-03T20:00:00Z",
		Updated:          time.Now().Format(time.RFC3339),
		KeyName:          "default-key",
		Flavor:           mockFlavorList[2],
		Locked:           false,
		PowerState:       "running",
		IPV4:             "192.168.1.102",
		AttachedDisks:    getRandomDisks(),
		AttachedNetworks: getRandomNetworks(),
		Loading:          rand.Intn(2) == 0,
	},
	{
		InstanceID:       "inst-004",
		ProjectID:        "proj-id-002",
		Name:             "test-server-4",
		Status:           "BUILDING",
		Created:          "2024-02-03T21:00:00Z",
		Updated:          time.Now().Format(time.RFC3339),
		KeyName:          "default-key",
		Flavor:           mockFlavorList[3],
		Locked:           false,
		PowerState:       "running",
		IPV4:             "192.168.1.103",
		AttachedDisks:    getRandomDisks(),
		AttachedNetworks: getRandomNetworks(),
		Loading:          rand.Intn(2) == 0,
	},
	{
		InstanceID:       "inst-005",
		ProjectID:        "proj-id-002",
		Name:             "test-server-5",
		Status:           "BUILDING",
		Created:          "2024-02-03T22:00:00Z",
		Updated:          time.Now().Format(time.RFC3339),
		KeyName:          "default-key",
		Flavor:           mockFlavorList[4],
		Locked:           false,
		PowerState:       "running",
		IPV4:             "192.168.1.104",
		AttachedDisks:    getRandomDisks(),
		AttachedNetworks: getRandomNetworks(),
		Loading:          rand.Intn(2) == 0,
	},
	{
		InstanceID:       "inst-006",
		ProjectID:        "proj-id-003",
		Name:             "test-server-6",
		Status:           "BUILDING",
		Created:          "2024-02-03T23:00:00Z",
		Updated:          time.Now().Format(time.RFC3339),
		KeyName:          "default-key",
		Flavor:           mockFlavorList[5],
		Locked:           false,
		PowerState:       "running",
		IPV4:             "192.168.1.105",
		AttachedDisks:    getRandomDisks(),
		AttachedNetworks: getRandomNetworks(),
		Loading:          rand.Intn(2) == 0,
	},
}

func mockInstanceLiveUpd() []*model.Instance {
	for i := range Instances {
		Instances[i].Status = possibleStatuses[rand.Intn(len(possibleStatuses))]
	}
	return Instances
}

func getRandomDisks() []*model.Disk {
	numDisks := rand.Intn(len(mockDiskList) + 1) // Random number of disks from 0 to len(mockDiskList)
	disks := []*model.Disk{}
	for i := 0; i < numDisks; i++ {
		disk := mockDiskList[rand.Intn(len(mockDiskList))]
		disks = append(disks, disk)
	}
	return disks
}

func getRandomNetworks() []*model.Network {
	numNetworks := rand.Intn(len(mockNetworks) + 1) // Random number of networks from 0 to len(mockNetworks)
	networks := []*model.Network{}
	for i := 0; i < numNetworks; i++ {
		network := mockNetworks[rand.Intn(len(mockNetworks))]
		networks = append(networks, network)
	}
	return networks
}

var mockDiskList = []*model.Disk{
	{
		DiskID:    "disk-001",
		SizeGb:    50,
		Bootable:  true,
		Status:    "active",
		Instances: []*model.Instance{},
		Image:     mockImages[rand.Intn(len(mockImages))],
	},
	{
		DiskID:    "disk-002",
		SizeGb:    100,
		Bootable:  false,
		Status:    "active",
		Instances: []*model.Instance{},
		Image:     mockImages[rand.Intn(len(mockImages))],
	},
	{
		DiskID:    "disk-003",
		SizeGb:    200,
		Bootable:  true,
		Status:    "active",
		Instances: []*model.Instance{},
		Image:     mockImages[rand.Intn(len(mockImages))],
	},
	{
		DiskID:    "disk-004",
		SizeGb:    25,
		Bootable:  false,
		Status:    "active",
		Instances: []*model.Instance{},
		Image:     mockImages[rand.Intn(len(mockImages))],
	},
	{
		DiskID:    "disk-005",
		SizeGb:    75,
		Bootable:  true,
		Status:    "active",
		Instances: []*model.Instance{},
		Image:     mockImages[rand.Intn(len(mockImages))],
	},
	{
		DiskID:    "disk-006",
		SizeGb:    125,
		Bootable:  false,
		Status:    "active",
		Instances: []*model.Instance{},
		Image:     mockImages[rand.Intn(len(mockImages))],
	},
	{
		DiskID:    "disk-007",
		SizeGb:    175,
		Bootable:  true,
		Status:    "active",
		Instances: []*model.Instance{},
		Image:     mockImages[rand.Intn(len(mockImages))],
	},
	{
		DiskID:    "disk-008",
		SizeGb:    225,
		Bootable:  false,
		Status:    "active",
		Instances: []*model.Instance{},
		Image:     mockImages[rand.Intn(len(mockImages))],
	},
}

var mockSSHKeys = []*model.SSHKey{
	{
		Name:      "key-001",
		PublicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAABIwAAAQEAr...",
		Instances: []*model.Instance{},
	},
	{
		Name:      "key-002",
		PublicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAABIwAAAQEAr...",
		Instances: []*model.Instance{},
	},
}

var mockImages = []*model.Image{
	{
		ImageID: "img-001",
		Label:   "Ubuntu",
		OsVersions: []*model.ImageVersion{
			{VersionName: "20.04", ImageVerID: "img-ver-001"},
			{VersionName: "22.04", ImageVerID: "img-ver-005"},
			{VersionName: "18.04", ImageVerID: "img-ver-006"},
		},
		CPU: &model.MinRec{Min: 2, Rec: 4},
		RAMGb: &model.MinRec{
			Min: 4,
			Rec: 8,
		},
		DiskGb: &model.MinRec{
			Min: 20,
			Rec: 40,
		},
	},
	{
		ImageID: "img-002",
		Label:   "CentOS",
		OsVersions: []*model.ImageVersion{
			{
				VersionName: "7.9",
				ImageVerID:  "img-ver-002",
			},
			{
				VersionName: "8",
				ImageVerID:  "img-ver-007",
			},
			{
				VersionName: "9",
				ImageVerID:  "img-ver-008",
			},
		},
		CPU: &model.MinRec{
			Min: 4,
			Rec: 8,
		},
		RAMGb: &model.MinRec{
			Min: 8,
			Rec: 16,
		},
		DiskGb: &model.MinRec{
			Min: 40,
			Rec: 80,
		},
	},
	{
		ImageID: "img-003",
		Label:   "Debian",
		OsVersions: []*model.ImageVersion{
			{
				VersionName: "11",
				ImageVerID:  "img-ver-003",
			},
			{
				VersionName: "10",
				ImageVerID:  "img-ver-009",
			},
			{
				VersionName: "9",
				ImageVerID:  "img-ver-010",
			},
		},
		CPU: &model.MinRec{
			Min: 2,
			Rec: 4,
		},
		RAMGb: &model.MinRec{
			Min: 4,
			Rec: 8,
		},
		DiskGb: &model.MinRec{
			Min: 20,
			Rec: 40,
		},
	},
	{
		ImageID: "img-004",
		Label:   "Fedora",
		OsVersions: []*model.ImageVersion{
			{
				VersionName: "35",
				ImageVerID:  "img-ver-004",
			},
			{
				VersionName: "36",
				ImageVerID:  "img-ver-011",
			},
			{
				VersionName: "37",
				ImageVerID:  "img-ver-012",
			},
		},
		CPU: &model.MinRec{
			Min: 4,
			Rec: 8,
		},
		RAMGb: &model.MinRec{
			Min: 8,
			Rec: 16,
		},
		DiskGb: &model.MinRec{
			Min: 40,
			Rec: 80,
		},
	},
}

var mockNetworks = []*model.Network{
	{
		NetworkID:        "net-001",
		NetworkName:      "public-network-1",
		Cidr:             "192.168.1.0/24",
		GatewayIP:        "192.168.1.1",
		IsPublic:         true,
		IPV4:             "192.168.1.10",
		AvailabilityZone: "az-1",
		Region:           "region-1",
		SecurityGroupID:  "sg-001",
	},
	{
		NetworkID:        "net-002",
		NetworkName:      "private-network-1",
		Cidr:             "10.0.0.0/24",
		GatewayIP:        "10.0.0.1",
		IsPublic:         false,
		IPV4:             "10.0.0.10",
		AvailabilityZone: "az-2",
		Region:           "region-2",
		SecurityGroupID:  "sg-002",
	},
	{
		NetworkID:        "net-003",
		NetworkName:      "public-network-2",
		Cidr:             "192.168.2.0/24",
		GatewayIP:        "192.168.2.1",
		IsPublic:         true,
		IPV4:             "192.168.2.10",
		AvailabilityZone: "az-1",
		Region:           "region-1",
		SecurityGroupID:  "sg-003",
	},
	{
		NetworkID:        "net-004",
		NetworkName:      "private-network-2",
		Cidr:             "10.0.1.0/24",
		GatewayIP:        "10.0.1.1",
		IsPublic:         false,
		IPV4:             "10.0.1.10",
		AvailabilityZone: "az-2",
		Region:           "region-2",
		SecurityGroupID:  "sg-004",
	},
	{
		NetworkID:        "net-005",
		NetworkName:      "public-network-3",
		Cidr:             "192.168.3.0/24",
		GatewayIP:        "192.168.3.1",
		IsPublic:         true,
		IPV4:             "192.168.3.10",
		AvailabilityZone: "az-3",
		Region:           "region-3",
		SecurityGroupID:  "sg-005",
	},
	{
		NetworkID:        "net-006",
		NetworkName:      "private-network-3",
		Cidr:             "10.0.2.0/24",
		GatewayIP:        "10.0.2.1",
		IsPublic:         false,
		IPV4:             "10.0.2.10",
		AvailabilityZone: "az-2",
		Region:           "region-2",
		SecurityGroupID:  "sg-006",
	},
	{
		NetworkID:        "net-007",
		NetworkName:      "public-network-4",
		Cidr:             "192.168.4.0/24",
		GatewayIP:        "192.168.4.1",
		IsPublic:         true,
		IPV4:             "192.168.4.10",
		AvailabilityZone: "az-1",
		Region:           "region-1",
		SecurityGroupID:  "sg-007",
	},
	{
		NetworkID:        "net-008",
		NetworkName:      "private-network-4",
		Cidr:             "10.0.3.0/24",
		GatewayIP:        "10.0.3.1",
		IsPublic:         false,
		IPV4:             "10.0.3.10",
		AvailabilityZone: "az-2",
		Region:           "region-2",
		SecurityGroupID:  "sg-008",
	},
}
