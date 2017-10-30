package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/appscode/data"
	"github.com/appscode/log"
	"github.com/kr/pretty"
	"github.com/tamalsaha/go-oneliners"
	"github.com/taoh/linodego"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	RetryInterval = 5 * time.Second
	RetryTimeout  = 5 * time.Minute
)

var (
	ErrNotFound = errors.New("not found")

	client *linodego.Client

	kernel        = 0
	instanceImage = 0

	clusterName  = "c1"
	zone         = "3"
	sku          = "1"
	rootPassword = "tamal" // CHANGE_IT

	scriptName = "linode-demo"

	name = ""
)

type NodeInfo struct {
	Name       string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	ExternalID string `json:"externalID,omitempty" protobuf:"bytes,2,opt,name=externalID"`
	PublicIP   string `json:"publicIP,omitempty" protobuf:"bytes,3,opt,name=publicIP"`
	PrivateIP  string `json:"privateIP,omitempty" protobuf:"bytes,4,opt,name=privateIP"`
	DiskId     string `json:"diskID,omitempty" protobuf:"bytes,5,opt,name=diskID"`
}

func main() {
	client = linodego.NewClient(os.Getenv("LINODE_TOKEN"), nil)

	var err error
	kernel, err = detectKernel()
	if err != nil {
		log.Fatalln(err)
	}
	oneliners.FILE("Kernel = ", kernel)

	instanceImage, err = detectInstanceImage()
	if err != nil {
		log.Fatalln(err)
	}
	oneliners.FILE("InstanceImage = ", instanceImage)

	scriptId, err := createOrUpdateStackScript()
	if err != nil {
		log.Fatalln(err)
	}
	oneliners.FILE("scriptId = ", scriptId)

	err = createNode()
	if err != nil {
		log.Fatalln(err)
	}

	//err := createNode()
	//oneliners.FILE(err)
}

func detectKernel() (int, error) {
	resp, err := client.Avail.Kernels(map[string]string{
		"isKVM": "true",
	})
	if err != nil {
		return 0, err
	}
	kernelId := -1
	for _, d := range resp.Kernels {
		if d.IsPVOPS == 1 {
			if strings.HasPrefix(d.Label.String(), "Latest 64 bit") {
				return d.KernelId, nil
			}
			if strings.Contains(d.Label.String(), "x86_64") && d.KernelId > kernelId {
				kernelId = d.KernelId
			}
		}
	}
	if kernelId >= 0 {
		return kernelId, nil
	}
	return 0, errors.New("can't find Kernel")
}

func detectInstanceImage() (int, error) {
	resp, err := client.Avail.Distributions()
	if err != nil {
		return 0, err
	}
	for _, d := range resp.Distributions {
		if d.Is64Bit == 1 && d.Label.String() == "Ubuntu 16.04 LTS" {
			return d.DistributionId, nil
		}
	}
	return 0, errors.New("can't find `Ubuntu 16.04 LTS` image")
}

func waitForStatus(id, status int) error {
	attempt := 0
	return wait.PollImmediate(RetryInterval, RetryTimeout, func() (bool, error) {
		attempt++

		resp, err := client.Linode.List(id)
		if err != nil {
			return false, nil
		}
		if len(resp.Linodes) == 0 {
			return false, nil
		}
		server := resp.Linodes[0]
		oneliners.FILE(fmt.Printf("Attempt %v: Instance `%v` is in status `%s`", attempt, id, statusString(server.Status)))
		if server.Status == status {
			return true, nil
		}
		return false, nil
	})
}

func getStartupScriptID() (int, error) {
	scripts, err := client.StackScript.List(0)
	if err != nil {
		return 0, err
	}
	for _, s := range scripts.StackScripts {
		if s.Label.String() == scriptName {
			return s.StackScriptId, nil
		}
	}
	return 0, ErrNotFound
}

func createOrUpdateStackScript() (int, error) {
	script := fmt.Sprintf(`#! /bin/bash
# %s
apt-get update
`, time.Now().String())
	scripts, err := client.StackScript.List(0)
	if err != nil {
		return 0, err
	}
	for _, s := range scripts.StackScripts {
		if s.Label.String() == scriptName {
			resp, err := client.StackScript.Update(s.StackScriptId, map[string]string{
				"script": script,
			})
			if err != nil {
				return 0, err
			}
			oneliners.FILE("Stack script for role updated")
			return resp.StackScriptId.StackScriptId, nil
		}
	}

	resp, err := client.StackScript.Create(scriptName, strconv.Itoa(instanceImage), script, map[string]string{
		"Description": fmt.Sprintf("Startup script for of Cluster %s", clusterName),
	})
	if err != nil {
		return 0, err
	}
	oneliners.FILE("Stack script for created")
	return resp.StackScriptId.StackScriptId, nil
}

const (
	LinodeStatus_BeingCreated = -1
	LinodeStatus_BrandNew     = 0
	LinodeStatus_Running      = 1
	LinodeStatus_PoweredOff   = 2
)

/*
Status values are -1: Being Created, 0: Brand New, 1: Running, and 2: Powered Off.
*/
func statusString(status int) string {
	switch status {
	case LinodeStatus_BeingCreated:
		return "Being Created"
	case LinodeStatus_BrandNew:
		return "Brand New"
	case LinodeStatus_Running:
		return "Running"
	case LinodeStatus_PoweredOff:
		return "Powered Off"
	default:
		return strconv.Itoa(status)
	}
}

func createNode() error {
	dcId, err := strconv.Atoi(zone)
	if err != nil {
		return err
	}
	planId, err := strconv.Atoi(sku)
	if err != nil {
		return err
	}
	server, err := client.Linode.Create(dcId, planId, 0)
	if err != nil {
		return err
	}
	linodeId := server.LinodeId.LinodeId

	_, err = client.Ip.AddPrivate(linodeId)
	if err != nil {
		return err
	}
	err = waitForStatus(linodeId, LinodeStatus_BrandNew)
	if err != nil {
		return err
	}

	node := NodeInfo{
		Name:       "", // host.Label.String(),
		ExternalID: strconv.Itoa(linodeId),
	}
	ips, err := client.Ip.List(linodeId, -1)
	if err != nil {
		return err
	}
	for _, ip := range ips.FullIPAddresses {
		if ip.IsPublic == 1 {
			node.PublicIP = ip.IPAddress
		} else {
			node.PrivateIP = ip.IPAddress
		}
	}
	oneliners.FILE(fmt.Sprintf("Node = %v", pretty.Formatter(node)))

	parts := strings.SplitN(node.PublicIP, ".", 4)
	node.Name = fmt.Sprintf("%s-%03s-%03s-%03s-%03s", clusterName, parts[0], parts[1], parts[2], parts[3])
	fmt.Printf("%03s-%03d-%03d-%03d", "1", 2, 3, 4)

	_, err = client.Linode.Update(linodeId, map[string]interface{}{
		"Label": node.Name,
	})
	if err != nil {
		return err
	}
	os.Exit(1)

	scriptId, err := getStartupScriptID()
	if err != nil {
		return err
	}

	stackScriptUDFResponses := fmt.Sprintf(`{
  "cluster": "%v",
  "instance": "%v",
  "stack_script_id": "%v"
}`, clusterName, name, scriptId)

	mt, err := data.ClusterMachineType("linode", sku)
	if err != nil {
		return err
	}
	distributionID := instanceImage
	//, err := strconv.Atoi(instanceImage)
	//if err != nil {
	//	return err
	//}
	swapDiskSize := 512                // MB
	rootDiskSize := mt.Disk*1024 - 512 // MB
	args := map[string]string{
	// "rootSSHKey": string(SSHKey(ctx).PublicKey),
	}
	rootDisk, err := client.Disk.CreateFromStackscript(scriptId, linodeId, name, stackScriptUDFResponses, distributionID, rootDiskSize, rootPassword, args)
	if err != nil {
		return err
	}
	swapDisk, err := client.Disk.Create(linodeId, "swap", "swap-disk", swapDiskSize, nil)
	if err != nil {
		return err
	}

	kernelId := kernel
	// TODO: Boot to grub2 : kernel id 201
	//kernelId, err := strconv.Atoi(kernel)
	//if err != nil {
	//	return err
	//}
	config, err := client.Config.Create(linodeId, kernelId, name, map[string]string{
		"RootDeviceNum": "1",
		"DiskList":      fmt.Sprintf("%d,%d", rootDisk.DiskJob.DiskId, swapDisk.DiskJob.DiskId),
	})
	if err != nil {
		return err
	}
	jobResp, err := client.Linode.Boot(linodeId, config.LinodeConfigId.LinodeConfigId)
	if err != nil {
		return err
	}
	oneliners.FILE(fmt.Printf("Running linode boot job %v", jobResp.JobId.JobId))
	oneliners.FILE(fmt.Printf("Linode %v created", name))

	// return linodeId, config.LinodeConfigId.LinodeConfigId, err

	err = waitForStatus(linodeId, LinodeStatus_Running)
	if err != nil {
		return err
	}

	//node := api.NodeInfo{
	//	Name:       "", // host.Label.String(),
	//	ExternalID: strconv.Itoa(linodeId),
	//}
	//ips, err := client.Ip.List(linodeId, -1)
	//if err != nil {
	//	return  err
	//}
	//for _, ip := range ips.FullIPAddresses {
	//	if ip.IsPublic == 1 {
	//		node.PublicIP = ip.IPAddress
	//	} else {
	//		node.PrivateIP = ip.IPAddress
	//	}
	//}

	_, err = client.Linode.Update(linodeId, map[string]interface{}{
		"Label": name,
	})
	if err != nil {
		return err
	}

	return nil
}
