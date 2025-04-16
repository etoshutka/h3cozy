package cozystack

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gqlfed/instances/graph/model"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

// VMInstanceGVR определяет GroupVersionResource для VMInstance
var VMInstanceGVR = schema.GroupVersionResource{
	Group:    "apps.cozystack.io",
	Version:  "v1alpha1",
	Resource: "vminstances",
}

// InstanceManager управляет виртуальными машинами в CozyStack
type InstanceManager struct {
	namespace       string
	k8sClient       *kubernetes.Clientset
	dynamicClient   dynamic.Interface
	diskManager     *DiskManager
	instanceCache   map[string]*model.Instance
	cacheMutex      sync.RWMutex
	stateChangeChan chan interface{}
}

// NewInstanceManager создает новый менеджер виртуальных машин
func NewInstanceManager(kubeconfigPath, namespace string) (*InstanceManager, error) {
	// Создаем конфигурацию клиента Kubernetes
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
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

	// Создаем менеджер дисков
	diskManager, err := NewDiskManager(kubeconfigPath, namespace)
	if err != nil {
		return nil, fmt.Errorf("error creating disk manager: %v", err)
	}

	manager := &InstanceManager{
		namespace:       namespace,
		k8sClient:       clientset,
		dynamicClient:   dynamicClient,
		diskManager:     diskManager,
		instanceCache:   make(map[string]*model.Instance),
		stateChangeChan: make(chan interface{}, 100),
	}

	// Инициализируем кэш
	err = manager.refreshInstanceCache(context.Background())
	if err != nil {
		// Логируем ошибку, но продолжаем работу
		fmt.Printf("Warning: Failed to initialize instance cache: %v\n", err)
	}

	// Запускаем горутину для отслеживания изменений
	go manager.watchInstances()

	return manager, nil
}

// CreateInstance создает новую виртуальную машину
func (m *InstanceManager) CreateInstance(ctx context.Context, input model.NewInstanceInput) (*model.Instance, error) {
	// Генерируем имена ресурсов
	diskID := fmt.Sprintf("vmd-%s", input.ID)
	instanceID := fmt.Sprintf("vmi-%s", input.ID)

	// Проверяем, существует ли VM с таким ID
	_, err := m.dynamicClient.Resource(VMInstanceGVR).Namespace(m.namespace).Get(ctx, instanceID, metav1.GetOptions{})
	if err == nil {
		return nil, fmt.Errorf("instance with ID %s already exists", instanceID)
	}

	// Создаем виртуальный диск
	_, err = m.diskManager.CreateDisk(ctx, diskID, 20, input.ImageID)
	if err != nil {
		return nil, fmt.Errorf("failed to create disk: %v", err)
	}

	// Ждем некоторое время, чтобы диск начал создаваться
	time.Sleep(2 * time.Second)

	// Создаем базовый cloud-init конфиг
	cloudInit := generateCloudInit(input.Hostname)

	// Создаем объект ресурса VMInstance
	instanceObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps.cozystack.io/v1alpha1",
			"kind":       "VMInstance",
			"metadata": map[string]interface{}{
				"name":      instanceID,
				"namespace": m.namespace,
				"labels": map[string]interface{}{
					"app":        "cozystack-vm",
					"created-by": "graphql-api",
					"project-id": input.ID,
					"hostname":   input.Hostname,
					"region":     input.Region,
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

	// Создаем инстанс через API Kubernetes с использованием retry для надежности
	var createdInstance *unstructured.Unstructured

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var createErr error
		createdInstance, createErr = m.dynamicClient.Resource(VMInstanceGVR).Namespace(m.namespace).Create(ctx, instanceObject, metav1.CreateOptions{})
		return createErr
	})

	if err != nil {
		// Если создание VM не удалось, удаляем созданный диск
		m.diskManager.DeleteDisk(ctx, diskID)
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
			Ram:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "hi-freq":
		instanceFlavor = &model.HiFreqFlavor{
			OriginalName: input.InstanceType,
			Vcpus:        flavorDetails.VCPUs,
			Ram:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "premium":
		instanceFlavor = &model.PremiumFlavor{
			OriginalName: input.InstanceType,
			Vcpus:        flavorDetails.VCPUs,
			Ram:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "pro":
		instanceFlavor = &model.ProFlavor{
			OriginalName: input.InstanceType,
			Vcpus:        flavorDetails.VCPUs,
			Ram:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	}

	// Получаем информацию о созданном диске
	disk, err := m.diskManager.GetDisk(ctx, diskID)
	if err != nil {
		disk = &model.Disk{
			DiskID:   diskID,
			SizeGb:   20,
			Bootable: true,
			Status:   "CREATING",
		}
	}

	// Создаем базовую модель сети
	network := &model.Network{
		NetworkID:        "default-net",
		NetworkName:      "Default Network",
		Cidr:             "192.168.0.0/24",
		GatewayIP:        "192.168.0.1",
		IsPublic:         true,
		IPv4:             "",
		AvailabilityZone: input.Region,
		Region:           input.Region,
		SecurityGroupID:  "default-sg",
	}

	// Создаем модель инстанса
	instance := &model.Instance{
		InstanceID:       instanceID,
		ProjectID:        input.ID,
		Name:             input.Hostname,
		Status:           "PROVISIONING",
		Created:          time.Now().Format(time.RFC3339),
		Updated:          time.Now().Format(time.RFC3339),
		KeyName:          "", // Будет заполнено позже
		Locked:           false,
		Loading:          true,
		PowerState:       "STARTING",
		IPv4:             "", // Будет заполнено позже
		Flavor:           instanceFlavor,
		AttachedDisks:    []*model.Disk{disk},
		AttachedNetworks: []*model.Network{network},
	}

	// Добавляем в кэш
	m.cacheMutex.Lock()
	m.instanceCache[instanceID] = instance
	m.cacheMutex.Unlock()

	// Запускаем горутину для отслеживания статуса
	go m.monitorInstanceStatus(ctx, instanceID)

	return instance, nil
}

// DeleteInstance удаляет виртуальную машину
func (m *InstanceManager) DeleteInstance(ctx context.Context, instanceID string) (bool, error) {
	// Находим информацию об инстансе
	instance, err := m.GetInstanceItem(ctx, instanceID)
	if err != nil {
		return false, fmt.Errorf("failed to find instance: %v", err)
	}

	// Получаем список дисков для последующего удаления
	var diskIDs []string
	for _, disk := range instance.AttachedDisks {
		diskIDs = append(diskIDs, disk.DiskID)
	}

	// Удаляем VMInstance через API Kubernetes с использованием retry
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return m.dynamicClient.Resource(VMInstanceGVR).Namespace(m.namespace).Delete(ctx, instanceID, metav1.DeleteOptions{})
	})

	if err != nil {
		// Если инстанс не найден, считаем операцию успешной
		if strings.Contains(err.Error(), "not found") {
			// Удаляем из кэша
			m.cacheMutex.Lock()
			delete(m.instanceCache, instanceID)
			m.cacheMutex.Unlock()

			// Продолжаем с удалением дисков
		} else {
			return false, fmt.Errorf("failed to delete VM: %v", err)
		}
	}

	// Удаляем диски
	for _, diskID := range diskIDs {
		err := m.diskManager.DeleteDisk(ctx, diskID)
		if err != nil {
			// Логируем ошибку, но продолжаем
			fmt.Printf("Warning: failed to delete disk %s: %v\n", diskID, err)
		}
	}

	// Удаляем из кэша
	m.cacheMutex.Lock()
	delete(m.instanceCache, instanceID)
	m.cacheMutex.Unlock()

	// Отправляем уведомление об изменении
	select {
	case m.stateChangeChan <- struct{}{}:
		// Уведомление отправлено
	default:
		// Канал переполнен, пропускаем
	}

	return true, nil
}

// GetInstanceList возвращает список виртуальных машин для проекта
func (m *InstanceManager) GetInstanceList(ctx context.Context, projectID string) ([]*model.Instance, error) {
	// Обновляем кэш
	err := m.refreshInstanceCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh instance cache: %v", err)
	}

	// Фильтруем инстансы по projectID
	m.cacheMutex.RLock()
	defer m.cacheMutex.RUnlock()

	var instances []*model.Instance
	for _, instance := range m.instanceCache {
		if projectID == "" || instance.ProjectID == projectID {
			instances = append(instances, instance)
		}
	}

	return instances, nil
}

// GetInstanceItem возвращает информацию о конкретной виртуальной машине
func (m *InstanceManager) GetInstanceItem(ctx context.Context, instanceID string) (*model.Instance, error) {
	// Сначала проверяем кэш
	m.cacheMutex.RLock()
	cachedInstance, exists := m.instanceCache[instanceID]
	m.cacheMutex.RUnlock()

	if exists {
		return cachedInstance, nil
	}

	// Обновляем информацию об инстансе
	err := m.refreshInstanceItem(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh instance info: %v", err)
	}

	// Проверяем кэш ещё раз
	m.cacheMutex.RLock()
	defer m.cacheMutex.RUnlock()

	instance, exists := m.instanceCache[instanceID]
	if !exists {
		return nil, fmt.Errorf("instance not found: %s", instanceID)
	}

	return instance, nil
}

// GetStateChangeChan возвращает канал для подписки на изменения состояния
func (m *InstanceManager) GetStateChangeChan() <-chan interface{} {
	return m.stateChangeChan
}

// refreshInstanceCache обновляет кэш всех инстансов
func (m *InstanceManager) refreshInstanceCache(ctx context.Context) error {
	// Получаем список инстансов через API Kubernetes
	vmList, err := m.dynamicClient.Resource(VMInstanceGVR).Namespace(m.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list VMs: %v", err)
	}

	// Создаем новый кэш
	newCache := make(map[string]*model.Instance)

	// Обновляем кэш дисков
	disks, err := m.diskManager.ListDisks(ctx)
	if err != nil {
		fmt.Printf("Warning: Failed to refresh disk cache: %v\n", err)
		disks = []*model.Disk{}
	}

	// Создаем маппинг для быстрого доступа к дискам
	diskMap := make(map[string]*model.Disk)
	for _, disk := range disks {
		diskMap[disk.DiskID] = disk
	}

	// Обрабатываем каждый инстанс
	for _, vmObj := range vmList.Items {
		instance, err := m.convertToInstanceModel(ctx, &vmObj, diskMap)
		if err != nil {
			// Логируем ошибку и продолжаем
			fmt.Printf("Warning: Failed to convert VM to model: %v\n", err)
			continue
		}

		newCache[instance.InstanceID] = instance
	}

	// Обновляем кэш
	m.cacheMutex.Lock()
	m.instanceCache = newCache
	m.cacheMutex.Unlock()

	return nil
}

// refreshInstanceItem обновляет информацию о конкретном инстансе
func (m *InstanceManager) refreshInstanceItem(ctx context.Context, instanceID string) error {
	// Получаем инстанс через API Kubernetes
	vmObj, err := m.dynamicClient.Resource(VMInstanceGVR).Namespace(m.namespace).Get(ctx, instanceID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VM: %v", err)
	}

	// Получаем список дисков инстанса
	spec, found, err := unstructured.NestedMap(vmObj.Object, "spec")
	if err != nil || !found {
		return fmt.Errorf("spec not found in VM object")
	}

	disksData, found, err := unstructured.NestedSlice(spec, "disks")
	if err != nil || !found {
		disksData = []interface{}{}
	}

	// Создаем маппинг дисков
	diskMap := make(map[string]*model.Disk)

	for _, diskData := range disksData {
		diskMap, ok := diskData.(map[string]interface{})
		if !ok {
			continue
		}

		diskName, ok := diskMap["name"].(string)
		if !ok {
			continue
		}

		// Получаем информацию о диске
		disk, err := m.diskManager.GetDisk(ctx, diskName)
		if err == nil {
			diskMap[diskName] = disk
		}
	}

	// Преобразуем в модель инстанса
	instance, err := m.convertToInstanceModel(ctx, vmObj, diskMap)
	if err != nil {
		return fmt.Errorf("failed to convert VM to model: %v", err)
	}

	// Обновляем кэш
	m.cacheMutex.Lock()
	m.instanceCache[instanceID] = instance
	m.cacheMutex.Unlock()

	return nil
}

// monitorInstanceStatus отслеживает изменения статуса инстанса
func (m *InstanceManager) monitorInstanceStatus(ctx context.Context, instanceID string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeout := time.After(15 * time.Minute)

	for {
		select {
		case <-timeout:
			// Превышен таймаут ожидания
			return
		case <-ticker.C:
			// Обновляем информацию об инстансе
			err := m.refreshInstanceItem(ctx, instanceID)
			if err != nil {
				// Возможно инстанс удален, прекращаем мониторинг
				return
			}

			// Проверяем статус инстанса
			m.cacheMutex.RLock()
			instance, exists := m.instanceCache[instanceID]
			m.cacheMutex.RUnlock()

			if !exists {
				// Инстанс удален, прекращаем мониторинг
				return
			}

			// Если инстанс больше не в состоянии PROVISIONING или STARTING,
			// отправляем уведомление и завершаем мониторинг
			if instance.Status != "PROVISIONING" && instance.PowerState != "STARTING" {
				select {
				case m.stateChangeChan <- instance:
					// Уведомление отправлено
				default:
					// Канал переполнен, пропускаем
				}
				return
			}
		}
	}
}

// watchInstances отслеживает изменения в инстансах
func (m *InstanceManager) watchInstances() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx := context.Background()
			err := m.refreshInstanceCache(ctx)
			if err != nil {
				fmt.Printf("Failed to refresh instance cache: %v\n", err)
				continue
			}

			// Отправляем уведомление об изменениях
			select {
			case m.stateChangeChan <- struct{}{}:
				// Уведомление отправлено
			default:
				// Канал переполнен, пропускаем
			}
		}
	}
}

// convertToInstanceModel преобразует Kubernetes ресурс в модель инстанса
func (m *InstanceManager) convertToInstanceModel(ctx context.Context, vmObj *unstructured.Unstructured, diskMap map[string]*model.Disk) (*model.Instance, error) {
	metadata := vmObj.Object["metadata"].(map[string]interface{})
	spec, found, err := unstructured.NestedMap(vmObj.Object, "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("spec not found in VM object")
	}

	status, found, err := unstructured.NestedMap(vmObj.Object, "status")
	if !found || err != nil {
		status = make(map[string]interface{})
	}

	// Извлекаем имя инстанса
	instanceID := metadata["name"].(string)

	// Извлекаем метки
	labels, found, err := unstructured.NestedMap(metadata, "labels")
	if !found || err != nil {
		labels = make(map[string]interface{})
	}

	// Извлекаем id проекта
	projectID := ""
	if id, exists := labels["project-id"]; exists {
		projectID = fmt.Sprintf("%v", id)
	}

	// Извлекаем hostname
	hostname := instanceID
	if name, exists := labels["hostname"]; exists {
		hostname = fmt.Sprintf("%v", name)
	}

	// Извлекаем регион
	region := "default-region"
	if reg, exists := labels["region"]; exists {
		region = fmt.Sprintf("%v", reg)
	}

	// Извлекаем тип инстанса
	instanceType, found, _ := unstructured.NestedString(spec, "instanceType")
	if !found {
		instanceType = "u1.small" // Значение по умолчанию
	}

	// Определяем flavor на основе типа инстанса
	var instanceFlavor model.Flavor
	flavorCategory, flavorDetails := parseFlavorType(instanceType)

	switch flavorCategory {
	case "base":
		instanceFlavor = &model.BaseFlavor{
			OriginalName: instanceType,
			Vcpus:        flavorDetails.VCPUs,
			Ram:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "hi-freq":
		instanceFlavor = &model.HiFreqFlavor{
			OriginalName: instanceType,
			Vcpus:        flavorDetails.VCPUs,
			Ram:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "premium":
		instanceFlavor = &model.PremiumFlavor{
			OriginalName: instanceType,
			Vcpus:        flavorDetails.VCPUs,
			Ram:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	case "pro":
		instanceFlavor = &model.ProFlavor{
			OriginalName: instanceType,
			Vcpus:        flavorDetails.VCPUs,
			Ram:          flavorDetails.RAM,
			RubMonth:     flavorDetails.Price,
		}
	}

	// Извлекаем статус VM
	vmStatus := "UNKNOWN"
	if phase, exists := status["phase"]; exists {
		vmStatus = fmt.Sprintf("%v", phase)
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
	externalAccess, found, _ := unstructured.NestedMap(status, "externalAccess")
	if found {
		addresses, found, _ := unstructured.NestedSlice(externalAccess, "externalAddresses")
		if found && len(addresses) > 0 {
			ipv4 = fmt.Sprintf("%v", addresses[0])
		}
	}

	// Извлекаем время создания и обновления
	creationTime := metadata["creationTimestamp"].(string)

	// Извлекаем информацию о дисках
	var attachedDisks []*model.Disk
	disksData, found, _ := unstructured.NestedSlice(spec, "disks")
	if found {
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
			disk, exists := diskMap[diskName]
			if exists {
				attachedDisks = append(attachedDisks, disk)
			} else {
				// Если диск не найден в кэше, запрашиваем его через API
				disk, err := m.diskManager.GetDisk(ctx, diskName)
				if err == nil {
					attachedDisks = append(attachedDisks, disk)
				} else {
					// Если не удалось получить информацию о диске, создаем заглушку
					attachedDisks = append(attachedDisks, &model.Disk{
						DiskID:   diskName,
						SizeGb:   20, // Значение по умолчанию
						Bootable: true,
						Status:   "UNKNOWN",
					})
				}
			}
		}
	}

	// Создаем модель сети
	network := &model.Network{
		NetworkID:        "default-net",
		NetworkName:      "Default Network",
		Cidr:             "192.168.0.0/24",
		GatewayIP:        "192.168.0.1",
		IsPublic:         true,
		IPv4:             ipv4,
		AvailabilityZone: region,
		Region:           region,
		SecurityGroupID:  "default-sg",
	}

	// Создаем модель инстанса
	instance := &model.Instance{
		InstanceID:       instanceID,
		ProjectID:        projectID,
		Name:             hostname,
		Status:           apiStatus,
		Created:          creationTime,
		Updated:          time.Now().Format(time.RFC3339),
		KeyName:          "", // Не используется в CozyStack
		Locked:           false,
		Loading:          apiStatus == "PROVISIONING" || powerState == "STARTING",
		PowerState:       powerState,
		IPv4:             ipv4,
		Flavor:           instanceFlavor,
		AttachedDisks:    attachedDisks,
		AttachedNetworks: []*model.Network{network},
	}

	return instance, nil
}
