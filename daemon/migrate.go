package daemon

import (
	"encoding/json"
	"fmt"
	"github.com/hyperhq/hyper/client"
	"github.com/hyperhq/hyper/engine"
	"github.com/hyperhq/hyper/utils"
	"github.com/hyperhq/runv/hypervisor"
	"github.com/hyperhq/runv/hypervisor/network"
	"github.com/hyperhq/runv/hypervisor/qemu"
	"github.com/hyperhq/runv/hypervisor/types"
	"github.com/hyperhq/runv/hypervisor/pod"
	"github.com/hyperhq/runv/lib/glog"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (daemon *Daemon) CmdPodMigrate(job *engine.Job) error {
	if len(job.Args) == 0 {
		return fmt.Errorf("Can not execute 'migrate' command without podId")
	}

	podId := job.Args[0]
	desAddr := job.Args[1]
	code, cause, err := daemon.MigratePod(podId, desAddr)
	if err != nil {
		return err
	}

	v := &engine.Env{}
	v.Set("ID", podId)
	v.SetInt("Code", code)
	v.Set("Cause", cause)
	if _, err := v.WriteTo(job.Stdout); err != nil {
		return err
	}

	return nil
}

func (daemon *Daemon) CmdVmMigrate(job *engine.Job) error {
	if len(job.Args) == 0 {
		return fmt.Errorf("Can not execute 'migrate' command without podname and Addr")
	}
	podId := job.Args[0]
	desAddr := job.Args[1]
	code, cause, err := daemon.MigrateVm(podId, desAddr)
	if err != nil {
		return err
	}

	v := &engine.Env{}
	v.Set("ID", podId)
	v.SetInt("Code", code)
	v.Set("Cause", cause)
	if _, err := v.WriteTo(job.Stdout); err != nil {
		return err
	}
	return nil
}

func (daemon *Daemon) CmdVmRestore(job *engine.Job) error {
	podId := job.Args[0]
	status := job.Args[1]
	if status == "false" {
		daemon.ClearPodFromLocal(podId)
		return nil
	}
	code, cause, err := daemon.RestoreVmFromMigration(podId)
	if err != nil {
		return err
	}
	v := &engine.Env{}
	v.Set("ID", podId)
	v.SetInt("Code", code)
	v.Set("Cause", cause)
	if _, err := v.WriteTo(job.Stdout); err != nil {
		return err
	}
	return nil
}

func (daemon *Daemon) MigratePod(podId, desAddr string) (int, string, error) {
	var shareType = "nfs"
	podPackage := &PodMigratePackage{
		PodId: podId,
	}
	desIp, port, err := parseAddr(desAddr)
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	err = daemon.gatherPodPackageFromDb(podId, podPackage)
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	err = gatherPodPackageFromFile(podId, podPackage)
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	shareList, err := daemon.GetShareDirList(podPackage, shareType, desIp)
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	errCode, cause, err := daemon.sendPodPackageToRemoteDaemon(podPackage, desIp, port, shareList, shareType)
	if err != nil {
		RemoveShareDirList(shareList, shareType)
		return errCode, cause, err
	}
	return types.E_OK, "", nil
}

func (daemon *Daemon) MigrateVm(podId, targetAddr string) (int, string, error) {
	desIp, port, err := parseAddr(targetAddr)
	if err != nil {
		return -1, "", err
	}
	glog.V(1).Infof("now we are in the MigrateVm, it worked well ... for now")
	var Response *types.VmResponse
	daemon.PodList.Lock()
	glog.V(2).Infof("lock PodList")
	defer glog.V(2).Infof("unlock PodList")
	defer daemon.PodList.Unlock()
	Pod, ok := daemon.PodList.Get(podId)
	if !ok {
		glog.Errorf("Can not find pod(%s)", podId)
		return -1, "", fmt.Errorf("Can not find pod(%s)", podId)
	}
	vm := Pod.vm
	PodEvent, Status, err := daemon.GetVmChan(vm)
	if err != nil {
		return -1, err.Error(), err
	}
	defer vm.ReleaseResponseChan(Status)
	defer vm.ReleaseRequestChan(PodEvent)
	migrateEvent := &hypervisor.MigrateVmCommand{
		Protocol: "tcp",
		IP:       desIp,
		Port:     port,
	}
	PodEvent <- migrateEvent

	var migrationSuccess = true
	Response = <-Status
	glog.V(1).Infof("MigrateVm Got response: %d: %s", Response.Code, Response.Cause)
	if Response.Code == types.E_MIGRATE_TIMEOUT {
		migrationSuccess = false
	}
	errCode, cause, err := doMigrate(podId, desIp, migrationSuccess)
	// if only last step failed, resume local vm
	if err != nil && migrationSuccess {
		PodEvent, Status, err := daemon.GetVmChan(vm)
		if err != nil {
			return -1, err.Error(), err
		}
		defer vm.ReleaseResponseChan(Status)
		defer vm.ReleaseRequestChan(PodEvent)
		PodEvent <- &hypervisor.ResumeVmCommand{}
	}

    if err == nil{
        _, cause, err := daemon.CleanPod(podId)
        if err != nil{
            glog.V(1).Infof("Clean local pod failed: %s", cause)
        }
        glog.V(1).Infof("Clean local pod success")
    }
	return errCode, cause, err
}

func doMigrate(podId, desIp string, isSuccess bool) (int, string, error) {
	var (
		proto            = "tcp"
		addr             = desIp + ":1246"
		v     url.Values = url.Values{}
	)
	cli := client.NewHyperClient(proto, addr, nil)
	v.Set("podId", podId)
	if isSuccess {
		v.Set("isSuccess", "true")
	} else {
		v.Set("isSuccess", "false")
	}
	_, _, err := client.ReadBody(cli.Call("POST", "/vm/restore?"+v.Encode(), nil, nil))
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	out := engine.NewOutput()
	remoteInfo, err := out.AddEnv()
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	out.Close()
	errCode := remoteInfo.GetInt("Code")
	cause := remoteInfo.Get("Cause")
	if errCode != types.E_OK {
		return errCode, cause, fmt.Errorf(cause)
	}
	return types.E_OK, "", nil
}

func (daemon *Daemon) ClearPodFromLocal(podId string) {
	daemon.DeleteVmByPod(podId)
	daemon.DeletePodFromDB(podId)
	daemon.DeletePodContainerFromDB(podId)
	daemon.DeleteVolumeId(podId)

	daemon.PodList.Lock()
	glog.V(2).Infof("lock  PodList")
	defer glog.V(2).Infof("unlock PodList")
	defer daemon.PodList.Unlock()
	//FIXME Technically, I'm not sure the container's image is removed from devicemapper
	pod, ok := daemon.PodList.Get(podId)
	if !ok {
		return
	}
	for _, c := range pod.status.Containers {
		glog.V(1).Infof("Ready to clear container: %s", c.Id)
		if _, _, err := daemon.DockerCli.SendCmdDelete(c.Id); err != nil {
			glog.Warningf("Error to clear container: %s", err.Error())
		}
	}
	daemon.RemovePod(podId)
}

func (daemon *Daemon) RestoreVmFromMigration(podId string) (int, string, error) {
	daemon.PodList.Lock()
	glog.V(2).Infof("lock  PodList")
	defer glog.V(2).Infof("unlock PodList")
	defer daemon.PodList.Unlock()
	/*err = daemon.CreatePod(podStatus.PodId, podStatus.PodData, false)
	  if err != nil{
	      return types.E_FAILED, err.Error(), err
	  }*/
	vmId, err := daemon.DbGetVmByPod(podId)
	if err != nil {
		glog.V(1).Info(err.Error(), " for ", podId)
		return types.E_FAILED, err.Error(), err
	}
	p, _ := daemon.PodList.Get(podId)
	if err := p.AssociateVm(daemon, string(vmId)); err != nil {
		glog.V(1).Info("Some problem during associate vm %s to pod %s, %v", string(vmId), podId, err)
		return types.E_FAILED, err.Error(), err
	}
	return types.E_OK, "", nil
}

func (daemon *Daemon) GetShareDirList(podPackage *PodMigratePackage, shareType, desIp string) ([]string, error) {
	shareList := []string{}
	backendFilePath := filepath.Join(utils.HYPER_ROOT, "devicemapper", "devicemapper")
	switch shareType {
	case "nfs":
		// FIXME should share volume-pool is volume is specified in pod
		localIp, err := utils.GetIPOfEth0()
		if err != nil {
			return nil, err
		}
		desAddr := fmt.Sprintf("%s:%s", desIp, backendFilePath)
		err = utils.AddNFSShareDir(desAddr)
		if err != nil {
			return nil, err
		}
		localAddr := fmt.Sprintf("%s:%s", localIp, backendFilePath)
		shareList = append(shareList, localAddr)
	default:
		return nil, fmt.Errorf("not support shareType: %s", shareType)
	}
	return shareList, nil
}

func RemoveShareDirList(shareList []string, shareType string) error {
	switch shareType {
	case "nfs":
		for _, shareDir := range shareList {
			utils.RemoveNFSShareDir(shareDir)
		}
	default:
		return fmt.Errorf("not supported shareType: %s", shareType)
	}
	return nil
}

func (daemon *Daemon) gatherPodPackageFromDb(podId string, podPackage *PodMigratePackage) error {
	key := fmt.Sprintf("vm-%s", podId)
	vmId, err := daemon.db.Get([]byte(key), nil)
	if err != nil {
		return fmt.Errorf("Can't get vmId from leveldb %s", err.Error())
	}
	key = fmt.Sprintf("vmdata-%s", vmId)
	vmData, err := daemon.db.Get([]byte(key), nil)
	if err != nil {
		return fmt.Errorf("Can't get vmData from leveldb %s", err.Error())
	}
	key = fmt.Sprintf("pod-container-%s", podId)
	podContainers, err := daemon.db.Get([]byte(key), nil)
	if err != nil {
		return fmt.Errorf("Can't get podContainers from leveldb %s", err.Error())
	}

	key = fmt.Sprintf("pod-%s", podId)
	podData, err := daemon.db.Get([]byte(key), nil)
	if err != nil {
		return fmt.Errorf("Can't get podData from leveldb %s", err.Error())
	}

	podPackage.VmId = string(vmId)
	podPackage.VmData = string(vmData)
	podPackage.PodContainers = string(podContainers)
	podPackage.PodData = string(podData)

	return nil
}

func gatherPodPackageFromFile(podId string, podPackage *PodMigratePackage) error {
	containersRootPath := filepath.Join(utils.HYPER_ROOT, "containers")
	metadataPath := filepath.Join(utils.HYPER_ROOT, "devicemapper/metadata")
	containers := strings.Split(podPackage.PodContainers, ":")
	for _, cId := range containers {
		containerPackage := &ContainerPackage{Id: cId}

		idMetadataFile := filepath.Join(metadataPath, cId)
		err, metadata := readFile(idMetadataFile)
		if err != nil {
			return err
		}
		containerPackage.Metadata = metadata

		idMetadataInitFile := idMetadataFile + "-init"
		err, metadataInit := readFile(idMetadataInitFile)
		if err != nil {
			return err
		}
		containerPackage.MetadataInit = metadataInit

		containerPath := filepath.Join(containersRootPath, cId)

		configFile := filepath.Join(containerPath, "config.json")
		err, configData := readFile(configFile)
		if err != nil {
			return err
		}
		containerPackage.Config = configData

		hostConfigFile := filepath.Join(containerPath, "hostconfig.json")
		err, hostConfigData := readFile(hostConfigFile)
		if err != nil {
			return err
		}
		containerPackage.Hostconfig = hostConfigData

		podPackage.ContainerList = append(podPackage.ContainerList, containerPackage)
	}
	return nil
}

func (daemon *Daemon) sendPodPackageToRemoteDaemon(podPackage *PodMigratePackage, desIP, port string, shareList []string, shareType string) (int, string, error) {
	var (
		proto            = "tcp"
		addr             = desIP + ":1246"
		v     url.Values = url.Values{}
	)
	cli := client.NewHyperClient(proto, addr, nil)
	migrateData, err := json.Marshal(podPackage)
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	v.Set("migrateData", string(migrateData))
	//share storage type and dir list
	v.Set("shareType", shareType)
	v.Set("port", port)
	for _, shareDir := range shareList {
		v.Add("shareList", shareDir)
	}
	_, _, err = client.ReadBody(cli.Call("POST", "/pod/restore?"+v.Encode(), nil, nil))
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	out := engine.NewOutput()
	remoteInfo, err := out.AddEnv()
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	out.Close()
	errCode := remoteInfo.GetInt("Code")
	cause := remoteInfo.Get("Cause")
	if errCode != types.E_OK {
		return errCode, cause, fmt.Errorf(cause)
	}
	return errCode, "", nil
}

func readFile(filePath string) (error, string) {
	if _, err := os.Stat(filePath); err != nil && os.IsNotExist(err) {
		return err, ""
	}
	jsonData, err := ioutil.ReadFile(filePath)
	if err != nil {
		return err, ""
	}
	return nil, string(jsonData)
}

func parseAddr(targetAddr string) (string, string, error) {
	addrSlice := strings.Split(targetAddr, ":")
	switch len(addrSlice) {
	case 2:
		return addrSlice[0], addrSlice[1], nil
	default:
		return "", "", fmt.Errorf("Not a legal addr")
	}
}

func (daemon *Daemon) CmdPodRestore(job *engine.Job) error {
	if len(job.Args) == 0 {
		return fmt.Errorf("Can not restore pod without data")
	}

	migrateData := job.Args[0]
	port := job.Args[1]
	shareType := job.Args[2]
	shareList := job.Args[3]

	code, cause, err := daemon.RestorePodData(migrateData, shareType, port, []string{shareList})
	if err != nil {
		daemon.cleanLocalPodData(migrateData)
		return err
	}

	v := &engine.Env{}
	v.SetInt("Code", code)
	v.Set("Cause", cause)
	if _, err := v.WriteTo(job.Stdout); err != nil {
		return err
	}
	return nil
}

func (daemon *Daemon) cleanLocalPodData(migrateData string) {
	podPackage := &PodMigratePackage{}
	err := json.Unmarshal([]byte(migrateData), podPackage)
	if err != nil {
		return
	}
	clearPodPackageFromFile(podPackage)
	daemon.clearPodPackageFromDB(podPackage)
}

func (daemon *Daemon) RestorePodData(migrateData, shareType, port string, shareList []string) (int, string, error) {
	podPackage := &PodMigratePackage{}
	err := json.Unmarshal([]byte(migrateData), podPackage)
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}

	err = daemon.restorePodPackageToLocal(podPackage, shareList[0])
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}

	daemon.PodList.Lock()
	glog.V(2).Infof("lock PodList")
	defer glog.V(2).Infof("unlock PodList")
	defer daemon.PodList.Unlock()

	err = daemon.RestorePod(podPackage.PodId, podPackage.PodData)
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}

	parmList, err := getRestoreDeviceCommands(daemon, podPackage)
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}

	parmList = append(parmList, "-incoming", fmt.Sprintf("tcp:0.0.0.0:%s", port))
	err = daemon.StartQemuIncomingMode(podPackage, parmList)
	if err != nil {
		return types.E_FAILED, err.Error(), err
	}
	glog.V(1).Info("Wait for qemu start 1 second")
	time.Sleep(1000 * time.Millisecond)
	return types.E_OK, "", nil
}

func (daemon *Daemon) RestorePod(podId string, podData string) error {
	if ids, _ := daemon.GetPodContainersByName(podId); ids != nil {
		for _, id := range ids {
			err := daemon.DockerCli.LoadContainer(id)
			if err != nil {
				return err
			}
			glog.V(1).Infof("LoadContainer info %s", id)
		}
	}

	err := daemon.CreatePod(podId, podData, false)
	return err
}

func (daemon *Daemon) StartQemuIncomingMode(podPackage *PodMigratePackage, parmList []string) error {
	pInfo := &hypervisor.PersistInfo{}
	err := json.Unmarshal([]byte(podPackage.VmData), pInfo)
	if err != nil {
		return err
	}
	homeDir := hypervisor.BaseDir + "/" + pInfo.Id + "/"
	vmContext := &hypervisor.VmContext{
		Boot: &hypervisor.BootConfig{
			CPU:    pInfo.UserSpec.Resource.Vcpu,
			Memory: pInfo.UserSpec.Resource.Memory,
			Kernel: daemon.Kernel,
			Initrd: daemon.Initrd,
			Bios:   daemon.Bios,
			Cbfs:   daemon.Cbfs,
		},
		ShareDir:        homeDir + hypervisor.ShareDirTag,
		ConsoleSockName: homeDir + hypervisor.ConsoleSockName,
		TtySockName:     homeDir + hypervisor.TtySockName,
		HyperSockName:   homeDir + hypervisor.HyperSockName,
	}
	qmpSockName := homeDir + qemu.QmpSockName
	qc := &qemu.QemuContext{}
	if !strings.EqualFold(pInfo.DriverInfo["qmpSock"].(string), qmpSockName) {
		pInfo.DriverInfo["qmpSock"] = qmpSockName
	}
	err = os.MkdirAll(vmContext.ShareDir, 0755)
	if err != nil {
		return err
	}
	pid, err := qc.StartQemuIncomingMode(vmContext, parmList, qmpSockName)
	if err != nil {
		return err
	}
	//update qemu pid in vmData
	pInfo.DriverInfo["pid"] = pid
	vmData, err := json.Marshal(pInfo)
	if err != nil {
		return err
	}
	err = daemon.UpdateVmData(pInfo.Id, vmData)
	if err != nil {
		return err
	}
	return nil
}

func getRestoreDeviceCommands(daemon *Daemon, podPackage *PodMigratePackage) ([]string, error) {
	var maps []pod.UserContainerPort
	pInfo := &hypervisor.PersistInfo{}
	err := json.Unmarshal([]byte(podPackage.VmData), pInfo)
	if err != nil {
		return nil, err
	}
	cmdList := []string{}
	//collect block device command
	for _, vol := range pInfo.VolumeList {
		cmdList = append(cmdList,
			"-drive", fmt.Sprintf("file=%s,if=none,id=drive%d,format=%s,snapshot=on,cache=writeback", vol.Filename, vol.ScsiId, vol.Format),
			"-device", fmt.Sprintf("driver=scsi-hd,bus=scsi0.0,scsi-id=%d,drive=drive%d,id=scsi-disk%d", vol.ScsiId, vol.ScsiId, vol.ScsiId),
		)
	}

	for _, c := range pInfo.UserSpec.Containers {
		for _, m := range c.Ports {
			maps = append(maps, m)
		}
	}
	//collect network device command
	// FIXME actually I don't think persistence tap device is a secure way,
	// cause we only want this tap device is used by current qemu
	// and we need to unpersistence the tap and delete it after pod is removed
	for _, netConfig := range pInfo.NetworkList {
		tapName, err := network.AllocateTap(netConfig.IpAddr, maps)
		if err != nil {
			return nil, err
		}
		cmdList = append(cmdList,
			"-netdev", fmt.Sprintf("tap,id=eth%d,ifname=%s,downscript=no,script=no", netConfig.Index, tapName),
			"-device", fmt.Sprintf("virtio-net-pci,netdev=eth%d,bus=pci.0,addr=0x%d,id=eth%d", netConfig.Index, netConfig.PciAddr, netConfig.Index),
		)
	}
	return cmdList, nil
}

func (daemon *Daemon) restorePodPackageToLocal(podPackage *PodMigratePackage, shareDir string) error {
	// the order is important, cause we need update volume's path and write metadata to file,
	// then we can read metadata from file, create new device and update deviceId again.
	err := updateVmData(daemon, podPackage)
	if err != nil {
		return err
	}

	err = restorePodPackageToFile(podPackage)
	if err != nil {
		return err
	}

	err = daemon.Storage.(*DevMapperStorage).Restore(shareDir, podPackage.PodContainers)
	if err != nil {
		return err
	}

	err = daemon.restorePodPackageToDB(podPackage)
	if err != nil {
		return err
	}
	return nil
}

func (daemon *Daemon) restorePodPackageToDB(podPackage *PodMigratePackage) error {
	glog.V(1).Infof("Prepare to restore DB data from migration")
	//check whether podId and VmId exist in local hyper
	key := fmt.Sprintf("vmdata-%s", podPackage.VmId)
	_, err := daemon.db.Get([]byte(key), nil)
	if err == nil {
		return fmt.Errorf("VM: %s already exist in local hyper", podPackage.VmId)
	}
	_, err = daemon.db.Get([]byte("pod-"+podPackage.PodId), nil)
	if err == nil {
		return fmt.Errorf("Pod: %s already exist in local hyper", key)
	}

	//add all the pod status to local hyper db
	err = daemon.db.Put([]byte("pod-"+podPackage.PodId), []byte(podPackage.PodData), nil)
	if err != nil {
		return fmt.Errorf("Insert pod data failed: %s", err.Error())
	}
	key = fmt.Sprintf("pod-container-%s", podPackage.PodId)
	err = daemon.db.Put([]byte(key), []byte(podPackage.PodContainers), nil)
	if err != nil {
		return fmt.Errorf("Insert pod Containers failed: %s", err.Error())
	}
	err = daemon.UpdateVmByPod(podPackage.PodId, podPackage.VmId)
	if err != nil {
		return err
	}
	err = daemon.UpdateVmData(podPackage.VmId, []byte(podPackage.VmData))
	if err != nil {
		return err
	}
	return nil
}

func restorePodPackageToFile(podPackage *PodMigratePackage) error {
	glog.V(1).Infof("Prepare to restore local file from migration")
	containerList := podPackage.ContainerList
	containersRootPath := filepath.Join(utils.HYPER_ROOT, "containers")
	metadataPath := filepath.Join(utils.HYPER_ROOT, "devicemapper/metadata")
	for _, container := range containerList {
		idMetadataFile := filepath.Join(metadataPath, container.Id)
		err := writeFile(idMetadataFile, container.Metadata)
		if err != nil {
			return err
		}

		idMetadataInitFile := idMetadataFile + "-init"
		err = writeFile(idMetadataInitFile, container.MetadataInit)
		if err != nil {
			return err
		}

		containerPath := filepath.Join(containersRootPath, container.Id)

		err = os.Mkdir(containerPath, 0644)
		if err != nil {
			return err
		}

		configFile := filepath.Join(containerPath, "config.json")
		err = writeFile(configFile, container.Config)
		if err != nil {
			return err
		}

		hostConfigFile := filepath.Join(containerPath, "hostconfig.json")
		err = writeFile(hostConfigFile, container.Hostconfig)
		if err != nil {
			return err
		}
	}
	return nil
}

func (daemon *Daemon) clearPodPackageFromDB(podPackage *PodMigratePackage) {
	key := fmt.Sprintf("vmdata-%s", podPackage.VmId)
	daemon.db.Delete([]byte(key), nil)
	key = fmt.Sprintf("vm-%s", podPackage.PodId)
	daemon.db.Delete([]byte(key), nil)
	key = fmt.Sprintf("pod-container-%s", podPackage.PodId)
	daemon.db.Delete([]byte(key), nil)
	key = fmt.Sprintf("pod-%s", podPackage.PodId)
	daemon.db.Delete([]byte(key), nil)
}

func clearPodPackageFromFile(podPackage *PodMigratePackage) {
	containerList := podPackage.ContainerList
	containersRootPath := filepath.Join(utils.HYPER_ROOT, "containers")
	metadataPath := filepath.Join(utils.HYPER_ROOT, "devicemapper/metadata")
	for _, container := range containerList {
		idMetadataFile := filepath.Join(metadataPath, container.Id)
		os.Remove(idMetadataFile)
		idMetadataInitFile := idMetadataFile + "-init"
		os.Remove(idMetadataInitFile)
		containerPath := filepath.Join(containersRootPath, container.Id)
		os.RemoveAll(containerPath)
	}
}

func writeFile(filePath, content string) error {
	if _, err := os.Stat(filePath); err == nil {
		return fmt.Errorf("file %s already exist", filePath)
	}
	err := ioutil.WriteFile(filePath, []byte(content), 0666)
	if err != nil {
		return err
	}
	return nil
}

func updateVmData(daemon *Daemon, podPackage *PodMigratePackage) error {
	glog.V(1).Infof("Prepare to update Vm Data")
	info := &hypervisor.PersistInfo{}
	err := json.Unmarshal([]byte(podPackage.VmData), info)
	if err != nil {
		glog.Error("fail to unmarshal VmData")
		return err
	}
	dms := daemon.Storage.(*DevMapperStorage)
	var filename string
	for i, vol := range info.VolumeList {
		filename = vol.Name
		vol.Name = filepath.Join("/dev/mapper", dms.DevPrefix+"-"+filename[len(filename)-64:len(filename)])
		info.VolumeList[i].Name = vol.Name
		info.VolumeList[i].Filename = vol.Name
	}
	vmData, err := json.Marshal(info)
	if err != nil {
		return err
	}
	podPackage.VmData = string(vmData)
	return nil
}

func (daemon *Daemon) GetVmChan(vm *hypervisor.Vm) (chan hypervisor.VmEvent, chan *types.VmResponse, error) {
	RequestChan, err := vm.GetRequestChan()
	if err != nil {
		return nil, nil, err
	}

	ResponseChan, err := vm.GetResponseChan()
	if err != nil {
		defer vm.ReleaseRequestChan(RequestChan)
		return nil, nil, err
	}

	return RequestChan, ResponseChan, nil
}

type PodMigratePackage struct {
	PodId         string              `json:"podid"`
	VmId          string              `json:"vmid"`
	VmData        string              `json:"vmdata"`
	PodContainers string              `json:"podContainers"`
	PodData       string              `json:"poddata"`
	ContainerList []*ContainerPackage `json:"containerList"`
}

type ContainerPackage struct {
	Id           string
	Config       string
	Hostconfig   string
	Metadata     string
	MetadataInit string
}
