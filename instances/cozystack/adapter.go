package cozystack

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"gqlfed/instances/graph/model"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// CozyResource определяет ресурсы CozyStack в Kubernetes
var (
	// VMDisk resource
	VMDiskGVR = schema.GroupVersionResource{
		Group:    "apps.cozystack.io",
		Version:  "v1alpha1",
		Resource: "vmdisks",
	}

	// VMInstance resource
	VMInstanceGVR = schema.GroupVersionResource{
		Group:    "apps.cozystack.io",
		Version:  "v1alpha1",
		Resource: "vminstances",
	}
)

// CozyStackAdapter преобразует операции GraphQL в команды для CozyStack
type CozyStackAdapter struct {
	namespace       string
	k8sClient       *kubernetes.Clientset
	dynamicClient   dynamic.Interface
	instanceCache   map[string]*model.Instance
	diskCache       map[string]*model.Disk
	cacheMutex      sync.RWMutex
	stateChangeChan chan interface{}
}

// NewCozyStackAdapter создает новый адаптер
func NewCozyStackAdapter(kubeconfigPath, namespace string) (*CozyStackAdapter, error) {
	// Создаем конфигурацию Kubernetes
	var config *rest.Config
	var err error

	if kubeconfigPath != "" {
		// Используем kubeconfig файл, если указан
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		// Иначе пытаемся использовать in-cluster config
		config, err = rest.InClusterConfig()
	}

	if err != nil {
		return nil, fmt.Errorf("error building kubeconfig: %v", err)
	}

	// Создаем стандартный клиент Kubernetes
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating kubernetes client: %v", err)
	}

	// Создаем динамический клиент для работы с кастомными ресурсами
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating dynamic client: %v", err)
	}

	adapter := &CozyStackAdapter{
		namespace:       namespace,
		k8sClient:       clientset,
		dynamicClient:   dynamicClient,
		instanceCache:   make(map[string]*model.Instance),
		diskCache:       make(map[string]*model.Disk),
		stateChangeChan: make(chan interface{}, 100),
	}

	// Запускаем горутину для отслеживания изменений
	go adapter.watchResources()

	return adapter, nil
}

// CreateInstance создает новую виртуальную машину
func (a *CozyStackAdapter) CreateInstance(ctx context.Context, input model.NewInstanceInput) (*model.Instance, error) {
	// Создаем виртуальный диск (VMDisk)
	diskID := fmt.Sprintf("vmd-%s", input.ID)

	// Определяем URL образа для выбранного imageId
	imageURL := getImageURL(input.ImageID)

	// Создаем объект ресурса VMDisk
	diskObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps.cozystack.io/v1alpha1",
			"kind":       "VMDisk",
			"metadata": map[string]interface{}{
				"name":      diskID,
				"namespace": a.namespace,
				"labels": map[string]interface{}{
					"app":        "cozystack-vm",
					"created-by": "graphql-api",
					"project-id": input.ID,
				},
			},
			"spec": map[string]interface{}{
				"optical": false,
				"source": map[string]interface{}{
					"http": map[string]interface{}{
						"url": imageURL,
					},
				},
				"storage":      "20Gi",
				"storageClass": "replicated",
			},
		},
	}

	// Создаем диск через API Kubernetes
	_, err := a.dynamicClient.Resource(VMDiskGVR).Namespace(a.namespace).Create(ctx, diskObject, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create disk: %v", err)
	}

	// Ждем некоторое время, чтобы диск начал создаваться
	time.Sleep(2 * time.Second)

	// Создаем виртуальную машину (VMInstance)
	instanceID := fmt.Sprintf("vmi-%s", input.ID)

	// Базовый cloud-init конфиг для виртуальной машины
	cloudInit := generateCloudInit(input.Hostname)

	// Создаем объект ресурса VMInstance
	instanceObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps.cozystack.io/v1alpha1",
			"kind":       "VMInstance",
			"metadata": map[string]interface{}{
				"name":      instanceID,
				"namespace": a.namespace,
				"labels": map[string]interface{}{
					"app":        "cozystack-vm",
					"created-by": "graphql-api",
					"project-id": input.ID,
					"hostname":   input.Hostname,
				},
			},
			"spec": map[string]interface{}{
				"cloudInit":       cloudInit,
				"disks":           []interface{}{map[string]interface{}{"name": diskID}},
				"external":        true,
				"externalMethod":  "PortList",
				"externalPorts":   []interface{}{22},
				"instanceProfile": "ubuntu",
				"instanceType":    input.InstanceType,
				"running":         true,
			},
		},
	}

	// Создаем инстанс через API Kubernetes
	_, err = a.dynamicClient.Resource(VMInstanceGVR).Namespace(a.namespace).Create(ctx, instanceObject, metav1.CreateOptions{})
	if err != nil {
		// Если создание VM не удалось, удаляем созданный диск
		a.dynamicClient.Resource(VMDiskGVR).Namespace(a.namespace).Delete(ctx, diskID, metav1.DeleteOptions{})
		return nil, fmt.Errorf("failed to create VM: %v", err)
	}

	// Определяем тип Flavor на основе instanceType
	var instanceFlavor model.Flavor
	flavorCategory, flavorDetails := parseFlavorType(input.InstanceType)

	switch flavorCategory {
	case "base":
		instanceFlavor = &model.BaseFlavor{
			OriginalName: input.InstanceType,
			Vcpus:        flavorDetails.VCPUs,
			RAM:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "hi-freq":
		instanceFlavor = &model.HiFreqFlavor{
			OriginalName: input.InstanceType,
			Vcpus:        flavorDetails.VCPUs,
			RAM:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "premium":
		instanceFlavor = &model.PremiumFlavor{
			OriginalName: input.InstanceType,
			Vcpus:        flavorDetails.VCPUs,
			RAM:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "pro":
		instanceFlavor = &model.ProFlavor{
			OriginalName: input.InstanceType,
			Vcpus:        flavorDetails.VCPUs,
			RAM:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	}

	// Возвращаем объект Instance для GraphQL
	instance := &model.Instance{
		InstanceID: instanceID,
		ProjectID:  input.ID,
		Name:       input.Hostname,
		Status:     "PROVISIONING",
		Created:    time.Now().Format(time.RFC3339),
		Updated:    time.Now().Format(time.RFC3339),
		KeyName:    "", // будет заполнено позже
		Locked:     false,
		Loading:    true,
		PowerState: "STARTING",
		IPV4:       "", // будет заполнено позже
		Flavor:     instanceFlavor,
		AttachedDisks: []*model.Disk{
			{
				DiskID:   diskID,
				SizeGb:   20,
				Bootable: true,
				Status:   "CREATING",
			},
		},
		AttachedNetworks: []*model.Network{}, // будет заполнено позже
	}

	// Добавляем в кэш
	a.cacheMutex.Lock()
	a.instanceCache[instanceID] = instance
	a.cacheMutex.Unlock()

	// Запускаем горутину для отслеживания статуса
	go a.monitorInstanceStatus(ctx, instanceID)

	return instance, nil
}

// DeleteInstance удаляет виртуальную машину
func (a *CozyStackAdapter) DeleteInstance(ctx context.Context, instanceID string) (bool, error) {
	// Находим соответствующий диск перед удалением инстанса
	a.cacheMutex.RLock()
	instance, exists := a.instanceCache[instanceID]
	a.cacheMutex.RUnlock()

	var diskIDs []string
	if exists && len(instance.AttachedDisks) > 0 {
		for _, disk := range instance.AttachedDisks {
			diskIDs = append(diskIDs, disk.DiskID)
		}
	}

	// Удаляем VMInstance через API Kubernetes
	err := a.dynamicClient.Resource(VMInstanceGVR).Namespace(a.namespace).Delete(ctx, instanceID, metav1.DeleteOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to delete VM: %v", err)
	}

	// Удаляем диски после успешного удаления VM
	for _, diskID := range diskIDs {
		err := a.dynamicClient.Resource(VMDiskGVR).Namespace(a.namespace).Delete(ctx, diskID, metav1.DeleteOptions{})
		if err != nil {
			// Логируем ошибку, но продолжаем
			fmt.Printf("Warning: failed to delete disk %s: %v\n", diskID, err)
		}
	}

	// Удаляем из кэша
	a.cacheMutex.Lock()
	delete(a.instanceCache, instanceID)
	for _, diskID := range diskIDs {
		delete(a.diskCache, diskID)
	}
	a.cacheMutex.Unlock()

	return true, nil
}

// GetInstanceList возвращает список виртуальных машин для проекта
func (a *CozyStackAdapter) GetInstanceList(ctx context.Context, projectID string) ([]*model.Instance, error) {
	// Обновляем кэш
	err := a.refreshCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh cache: %v", err)
	}

	// Фильтруем инстансы по projectID
	a.cacheMutex.RLock()
	defer a.cacheMutex.RUnlock()

	var instances []*model.Instance
	for _, instance := range a.instanceCache {
		if projectID == "" || instance.ProjectID == projectID {
			instances = append(instances, instance)
		}
	}

	return instances, nil
}

// GetInstanceItem возвращает информацию о конкретной виртуальной машине
func (a *CozyStackAdapter) GetInstanceItem(ctx context.Context, instanceID string) (*model.Instance, error) {
	// Обновляем кэш для одного инстанса
	err := a.refreshInstanceCache(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh instance cache: %v", err)
	}

	a.cacheMutex.RLock()
	defer a.cacheMutex.RUnlock()

	instance, exists := a.instanceCache[instanceID]
	if !exists {
		return nil, fmt.Errorf("instance not found: %s", instanceID)
	}

	return instance, nil
}

// Вспомогательные методы

// refreshCache обновляет кэш инстансов и дисков
func (a *CozyStackAdapter) refreshCache(ctx context.Context) error {
	// Получаем список VMInstance через API Kubernetes
	vmList, err := a.dynamicClient.Resource(VMInstanceGVR).Namespace(a.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list VMs: %v", err)
	}

	// Получаем список VMDisk через API Kubernetes
	diskList, err := a.dynamicClient.Resource(VMDiskGVR).Namespace(a.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list disks: %v", err)
	}

	// Обновляем кэш дисков
	diskMap := make(map[string]*model.Disk)
	for _, diskObj := range diskList.Items {
		disk, err := convertToDiskModel(&diskObj)
		if err != nil {
			continue
		}
		diskMap[disk.DiskID] = disk
	}

	// Обновляем кэш инстансов
	instanceMap := make(map[string]*model.Instance)
	for _, vmObj := range vmList.Items {
		instance, err := a.convertToInstanceModel(&vmObj, diskMap)
		if err != nil {
			continue
		}
		instanceMap[instance.InstanceID] = instance
	}

	// Применяем обновления к кэшу
	a.cacheMutex.Lock()
	a.diskCache = diskMap
	a.instanceCache = instanceMap
	a.cacheMutex.Unlock()

	return nil
}

// refreshInstanceCache обновляет кэш для одного инстанса
func (a *CozyStackAdapter) refreshInstanceCache(ctx context.Context, instanceID string) error {
	// Получаем VMInstance через API Kubernetes
	vmObj, err := a.dynamicClient.Resource(VMInstanceGVR).Namespace(a.namespace).Get(ctx, instanceID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VM: %v", err)
	}

	// Обновляем информацию о дисках инстанса
	spec, found, err := unstructured.NestedMap(vmObj.Object, "spec")
	if err != nil || !found {
		return fmt.Errorf("failed to get VM spec: %v", err)
	}

	disksData, found, err := unstructured.NestedSlice(spec, "disks")
	if err != nil {
		return fmt.Errorf("failed to get disks info: %v", err)
	}

	// Обновляем диски в кэше
	for _, diskData := range disksData {
		diskMap, ok := diskData.(map[string]interface{})
		if !ok {
			continue
		}

		diskName, ok := diskMap["name"].(string)
		if !ok {
			continue
		}

		// Получаем информацию о диске из API
		diskObj, err := a.dynamicClient.Resource(VMDiskGVR).Namespace(a.namespace).Get(ctx, diskName, metav1.GetOptions{})
		if err != nil {
			continue
		}

		disk, err := convertToDiskModel(diskObj)
		if err != nil {
			continue
		}

		// Обновляем диск в кэше
		a.cacheMutex.Lock()
		a.diskCache[diskName] = disk
		a.cacheMutex.Unlock()
	}

	// Преобразуем в модель инстанса
	instance, err := a.convertToInstanceModel(vmObj, a.diskCache)
	if err != nil {
		return fmt.Errorf("failed to convert VM to model: %v", err)
	}

	// Обновляем инстанс в кэше
	a.cacheMutex.Lock()
	a.instanceCache[instanceID] = instance
	a.cacheMutex.Unlock()

	return nil
}

// monitorInstanceStatus отслеживает изменения статуса инстанса
func (a *CozyStackAdapter) monitorInstanceStatus(ctx context.Context, instanceID string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeout := time.After(10 * time.Minute)

	for {
		select {
		case <-timeout:
			// Превышен таймаут ожидания
			return
		case <-ticker.C:
			// Обновляем информацию об инстансе
			err := a.refreshInstanceCache(ctx, instanceID)
			if err != nil {
				// Возможно инстанс удален, прекращаем мониторинг
				return
			}

			// Проверяем статус инстанса
			a.cacheMutex.RLock()
			instance, exists := a.instanceCache[instanceID]
			a.cacheMutex.RUnlock()

			if !exists {
				// Инстанс удален, прекращаем мониторинг
				return
			}

			// Если инстанс больше не в состоянии PROVISIONING или STARTING,
			// отправляем уведомление и завершаем мониторинг
			if instance.Status != "PROVISIONING" && instance.PowerState != "STARTING" {
				select {
				case a.stateChangeChan <- instance:
					// Уведомление отправлено
				default:
					// Канал переполнен, пропускаем
				}
				return
			}
		}
	}
}

// watchResources отслеживает изменения в ресурсах Kubernetes
func (a *CozyStackAdapter) watchResources() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx := context.Background()
			err := a.refreshCache(ctx)
			if err != nil {
				fmt.Printf("Failed to refresh cache: %v\n", err)
				continue
			}

			// Отправляем уведомления о изменениях
			instances := make([]*model.Instance, 0, len(a.instanceCache))
			a.cacheMutex.RLock()
			for _, instance := range a.instanceCache {
				instances = append(instances, instance)
			}
			a.cacheMutex.RUnlock()

			if len(instances) > 0 {
				select {
				case a.stateChangeChan <- instances:
					// Уведомление отправлено
				default:
					// Канал переполнен, пропускаем
				}
			}
		}
	}
}

// GetStateChangeChan возвращает канал для подписки на изменения состояния
func (a *CozyStackAdapter) GetStateChangeChan() <-chan interface{} {
	return a.stateChangeChan
}

// convertToInstanceModel преобразует Kubernetes ресурс в модель инстанса
func (a *CozyStackAdapter) convertToInstanceModel(vmObj *unstructured.Unstructured, diskCache map[string]*model.Disk) (*model.Instance, error) {
	metadata := vmObj.Object["metadata"].(map[string]interface{})
	spec, found, err := unstructured.NestedMap(vmObj.Object, "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("spec not found in VM object")
	}

	status, found, err := unstructured.NestedMap(vmObj.Object, "status")
	if err != nil {
		status = make(map[string]interface{})
	}

	// Извлекаем имя инстанса
	instanceID := metadata["name"].(string)

	// Извлекаем метки
	labels, found, err := unstructured.NestedMap(metadata, "labels")
	if err != nil {
		labels = make(map[string]interface{})
	}

	// Извлекаем имя проекта
	projectID := ""
	if id, exists := labels["project-id"]; exists {
		projectID = id.(string)
	}

	// Извлекаем hostname
	hostname := instanceID
	if name, exists := labels["hostname"]; exists {
		hostname = name.(string)
	}

	// Извлекаем тип инстанса
	instanceType, _, _ := unstructured.NestedString(spec, "instanceType")

	// Определяем flavor на основе типа инстанса
	var instanceFlavor model.Flavor
	flavorCategory, flavorDetails := parseFlavorType(instanceType)

	switch flavorCategory {
	case "base":
		instanceFlavor = &model.BaseFlavor{
			OriginalName: instanceType,
			Vcpus:        flavorDetails.VCPUs,
			RAM:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "hi-freq":
		instanceFlavor = &model.HiFreqFlavor{
			OriginalName: instanceType,
			Vcpus:        flavorDetails.VCPUs,
			RAM:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "premium":
		instanceFlavor = &model.PremiumFlavor{
			OriginalName: instanceType,
			Vcpus:        flavorDetails.VCPUs,
			RAM:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "pro":
		instanceFlavor = &model.ProFlavor{
			OriginalName: instanceType,
			Vcpus:        flavorDetails.VCPUs,
			RAM:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	}

	// Извлекаем статус VM
	vmStatus := "UNKNOWN"
	if phase, exists := status["phase"]; exists {
		vmStatus = phase.(string)
	}

	// Маппинг статусов CozyStack -> GraphQL API
	statusMap := map[string]string{
		"Creating":  "PROVISIONING",
		"Pending":   "PROVISIONING",
		"Running":   "ACTIVE",
		"Succeeded": "ACTIVE",
		"Failed":    "ERROR",
		"Unknown":   "UNKNOWN",
	}

	powerStateMap := map[string]string{
		"Creating":  "STARTING",
		"Pending":   "STARTING",
		"Running":   "ACTIVE",
		"Succeeded": "ACTIVE",
		"Failed":    "STOPPED",
		"Unknown":   "UNKNOWN",
	}

	apiStatus, ok := statusMap[vmStatus]
	if !ok {
		apiStatus = "UNKNOWN"
	}

	powerState, ok := powerStateMap[vmStatus]
	if !ok {
		powerState = "UNKNOWN"
	}

	// Извлекаем IP-адрес
	ipv4 := ""
	if externalAccess, exists := status["externalAccess"].(map[string]interface{}); exists {
		if addresses, exists := externalAccess["externalAddresses"].([]interface{}); exists && len(addresses) > 0 {
			ipv4 = addresses[0].(string)
		}
	}

	// Извлекаем информацию о дисках
	var attachedDisks []*model.Disk
	disksData, found, err := unstructured.NestedSlice(spec, "disks")
	if err == nil && found {
		for _, diskData := range disksData {
			diskMap, ok := diskData.(map[string]interface{})
			if !ok {
				continue
			}

			diskName, ok := diskMap["name"].(string)
			if !ok {
				continue
			}

			// Ищем диск в кэше
			disk, exists := diskCache[diskName]
			if exists {
				attachedDisks = append(attachedDisks, disk)
			} else {
				// Если диск не найден в кэше, создаем заглушку
				attachedDisks = append(attachedDisks, &model.Disk{
					DiskID:   diskName,
					SizeGb:   20, // Значение по умолчанию
					Bootable: true,
					Status:   "UNKNOWN",
				})
			}
		}
	}

	// Извлекаем время создания и обновления
	creationTime := metadata["creationTimestamp"].(string)

	// Создаем модель инстанса
	instance := &model.Instance{
		InstanceID:    instanceID,
		ProjectID:     projectID,
		Name:          hostname,
		Status:        apiStatus,
		Created:       creationTime,
		Updated:       time.Now().Format(time.RFC3339),
		KeyName:       "", // На данный момент не используется
		Locked:        false,
		Loading:       apiStatus == "PROVISIONING" || powerState == "STARTING",
		PowerState:    powerState,
		IPV4:          ipv4,
		Flavor:        instanceFlavor,
		AttachedDisks: attachedDisks,
	}

	return instance, nil
}

// Структура для хранения информации о flavor
type FlavorInfo struct {
	VCPUs string
	RAM   string
	Price string
}

// parseFlavorType определяет категорию и детали flavor на основе типа инстанса
func parseFlavorType(instanceType string) (string, FlavorInfo) {
	flavorMap := map[string]map[string]FlavorInfo{
		"base": {
			"u1.xsmall":   {VCPUs: "1", RAM: "2Gi", Price: "500"},
			"u1.small":    {VCPUs: "1", RAM: "4Gi", Price: "700"},
			"u1.medium":   {VCPUs: "2", RAM: "4Gi", Price: "900"},
			"u1.2xmedium": {VCPUs: "2", RAM: "8Gi", Price: "1200"},
			"u1.large":    {VCPUs: "4", RAM: "8Gi", Price: "1600"},
		},
		"hi-freq": {
			"uf1.small":  {VCPUs: "1", RAM: "4Gi", Price: "1000"},
			"uf1.medium": {VCPUs: "2", RAM: "8Gi", Price: "1800"},
			"uf1.large":  {VCPUs: "4", RAM: "16Gi", Price: "3000"},
		},
		"premium": {
			"p1.medium": {VCPUs: "2", RAM: "8Gi", Price: "2000"},
			"p1.large":  {VCPUs: "4", RAM: "16Gi", Price: "3500"},
			"p1.xlarge": {VCPUs: "8", RAM: "32Gi", Price: "6000"},
		},
		"pro": {
			"pro1.large":  {VCPUs: "4", RAM: "32Gi", Price: "5000"},
			"pro1.xlarge": {VCPUs: "8", RAM: "64Gi", Price: "9000"},
		},
	}

	// Определяем категорию по префиксу
	var category string
	var info FlavorInfo

	if instanceType[:2] == "uf" {
		category = "hi-freq"
	} else if instanceType[:1] == "p" {
		category = "premium"
	} else if instanceType[:3] == "pro" {
		category = "pro"
	} else {
		category = "base"
	}

	// Ищем детали в соответствующей категории
	if categoryMap, exists := flavorMap[category]; exists {
		if details, exists := categoryMap[instanceType]; exists {
			info = details
		} else {
			// Значения по умолчанию
			info = FlavorInfo{VCPUs: "2", RAM: "4Gi", Price: "1000"}
		}
	} else {
		// Значения по умолчанию
		info = FlavorInfo{VCPUs: "2", RAM: "4Gi", Price: "1000"}
	}

	return category, info
}

// getImageURL возвращает URL образа по ID
func getImageURL(imageID string) string {
	imageMap := map[string]string{
		"ubuntu-24-04-noble": "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		"ubuntu-22-04-jammy": "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
		"debian-12-bookworm": "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2",
		"centos-stream-9":    "https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2",
		"almalinux-9":        "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2",
		"fedora-38":          "https://download.fedoraproject.org/pub/fedora/linux/releases/38/Cloud/x86_64/images/Fedora-Cloud-Base-38-1.6.x86_64.qcow2",
		"opensuse-leap-15-5": "https://download.opensuse.org/distribution/leap/15.5/appliances/openSUSE-Leap-15.5-JeOS.x86_64-15.5.0-OpenStack-Cloud.qcow2",
	}

	// Возвращаем URL образа или URL образа Ubuntu по умолчанию
	if url, exists := imageMap[imageID]; exists {
		return url
	}

	return "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
}

// Функция для преобразования Kubernetes ресурса в модель диска
func convertToDiskModel(diskObj *unstructured.Unstructured) (*model.Disk, error) {
	metadata := diskObj.Object["metadata"].(map[string]interface{})
	spec, found, err := unstructured.NestedMap(diskObj.Object, "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("spec not found in Disk object")
	}

	status, found, err := unstructured.NestedMap(diskObj.Object, "status")
	if err != nil {
		status = make(map[string]interface{})
	}

	// Извлекаем имя диска
	diskID := metadata["name"].(string)

	// Извлекаем размер диска
	storageStr, found, _ := unstructured.NestedString(spec, "storage")
	sizeGB := 20 // Размер по умолчанию

	if found && len(storageStr) > 2 && storageStr[len(storageStr)-2:] == "Gi" {
		sizeStr := storageStr[:len(storageStr)-2]
		size, err := strconv.Atoi(sizeStr)
		if err == nil {
			sizeGB = size
		}
	}

	// Извлекаем статус диска
	diskStatus := "UNKNOWN"
	if phase, exists := status["phase"]; exists {
		phaseStr := phase.(string)

		// Маппинг статусов CozyStack -> GraphQL API
		statusMap := map[string]string{
			"Creating":  "CREATING",
			"Pending":   "CREATING",
			"Ready":     "ACTIVE",
			"Succeeded": "ACTIVE",
			"Failed":    "ERROR",
			"Unknown":   "UNKNOWN",
		}

		if mappedStatus, ok := statusMap[phaseStr]; ok {
			diskStatus = mappedStatus
		} else {
			diskStatus = phaseStr
		}
	}

	// Создаем модель диска
	disk := &model.Disk{
		DiskID:   diskID,
		SizeGb:   int32(sizeGB),
		Bootable: true, // Предполагаем, что все диски загрузочные
		Status:   diskStatus,
	}

	return disk, nil
}

// Функция для генерации базового cloud-init конфига
func generateCloudInit(hostname string) string {
	return fmt.Sprintf(`#cloud-config
hostname: %s
fqdn: %s.local
# Set password and SSH key for user
users:
- name: ubuntu
  sudo: ALL=(ALL) NOPASSWD:ALL
  groups: users, admin
  shell: /bin/bash
  lock_passwd: false
  # Password is "123321"
  passwd: "$6$rounds=4096$YVwcWswAL5YlQ7J8$lBiL.tYG/xwAmS8k6v15OFcMLhOCTTcz1658cZQ2YuS0P/ja6C.6yZ6nGeD3Z60eZrgg1gvC3DxgXT4znUxzz0"
  ssh_authorized_keys:
  - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEUuKKIQxEP7liDMDLc2b3HnF5vf5wcOTJmk4MPFiaDg user@example.com
# Set timezone and locale
timezone: UTC
locale: en_US.UTF-8
# Install packages
packages:
- htop
- curl
- net-tools
- ca-certificates
# Run commands
runcmd:
- touch /var/log/cloud-init-completed.log
- echo "Cloud-init setup complete!" > /var/log/cloud-init-completed.log
# Enable SSH password authentication
ssh_pwauth: true
# Update and upgrade packages
package_update: true
package_upgrade: true
`, hostname, hostname)
}
