package cozystack

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"gqlfed/instances/graph/model"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

// DiskManager управляет виртуальными дисками в CozyStack
type DiskManager struct {
	namespace     string
	dynamicClient dynamic.Interface
	diskCache     map[string]*model.Disk
	cacheMutex    sync.RWMutex
	imageURLs     map[string]string
}

// NewDiskManager создает новый менеджер дисков
func NewDiskManager(kubeconfigPath, namespace string) (*DiskManager, error) {
	// Создаем конфигурацию клиента Kubernetes
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("error building kubeconfig: %v", err)
	}

	// Создаем динамический клиент для работы с кастомными ресурсами
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating dynamic client: %v", err)
	}

	manager := &DiskManager{
		namespace:     namespace,
		dynamicClient: dynamicClient,
		diskCache:     make(map[string]*model.Disk),
		imageURLs:     initializeImageURLs(),
	}

	// Инициализируем кэш
	err = manager.refreshDiskCache(context.Background())
	if err != nil {
		// Логируем ошибку, но продолжаем работу
		fmt.Printf("Warning: Failed to initialize disk cache: %v\n", err)
	}

	// Запускаем горутину для периодического обновления кэша
	go manager.periodicCacheRefresh()

	return manager, nil
}

// VMDiskGVR определяет GroupVersionResource для VMDisk
var VMDiskGVR = schema.GroupVersionResource{
	Group:    "apps.cozystack.io",
	Version:  "v1alpha1",
	Resource: "vmdisks",
}

// CreateDisk создает новый виртуальный диск
func (m *DiskManager) CreateDisk(ctx context.Context, diskID string, sizeGB int, imageID string) (*model.Disk, error) {
	// Проверяем, существует ли диск с таким ID
	_, err := m.dynamicClient.Resource(VMDiskGVR).Namespace(m.namespace).Get(ctx, diskID, metav1.GetOptions{})
	if err == nil {
		return nil, fmt.Errorf("disk with ID %s already exists", diskID)
	}

	// Получаем URL образа для выбранного imageID
	imageURL, ok := m.imageURLs[imageID]
	if !ok {
		// Если образ не найден, используем образ Ubuntu по умолчанию
		imageURL = "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
	}

	// Создаем объект ресурса VMDisk
	diskObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps.cozystack.io/v1alpha1",
			"kind":       "VMDisk",
			"metadata": map[string]interface{}{
				"name":      diskID,
				"namespace": m.namespace,
				"labels": map[string]interface{}{
					"app":        "cozystack-vm",
					"created-by": "graphql-api",
				},
			},
			"spec": map[string]interface{}{
				"optical": false,
				"source": map[string]interface{}{
					"http": map[string]interface{}{
						"url": imageURL,
					},
				},
				"storage":      fmt.Sprintf("%dGi", sizeGB),
				"storageClass": "replicated",
			},
		},
	}

	// Используем retry для повышения надежности создания ресурса
	var createdDisk *unstructured.Unstructured

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var createErr error
		createdDisk, createErr = m.dynamicClient.Resource(VMDiskGVR).Namespace(m.namespace).Create(ctx, diskObject, metav1.CreateOptions{})
		return createErr
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create disk: %v", err)
	}

	// Создаем модель диска
	disk := &model.Disk{
		DiskID:   diskID,
		SizeGb:   sizeGB,
		Bootable: true,
		Status:   "CREATING",
	}

	// Сохраняем в кэш
	m.cacheMutex.Lock()
	m.diskCache[diskID] = disk
	m.cacheMutex.Unlock()

	// Запускаем горутину для отслеживания создания диска
	go m.waitForDiskReady(ctx, diskID)

	return disk, nil
}

// DeleteDisk удаляет виртуальный диск
func (m *DiskManager) DeleteDisk(ctx context.Context, diskID string) error {
	// Используем retry для повышения надежности удаления ресурса
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := m.dynamicClient.Resource(VMDiskGVR).Namespace(m.namespace).Delete(ctx, diskID, metav1.DeleteOptions{})
		if err != nil {
			// Если диск не найден, считаем операцию успешной
			if strings.Contains(err.Error(), "not found") {
				// Удаляем из кэша
				m.cacheMutex.Lock()
				delete(m.diskCache, diskID)
				m.cacheMutex.Unlock()
				return nil
			}
			return err
		}

		// Удаляем из кэша
		m.cacheMutex.Lock()
		delete(m.diskCache, diskID)
		m.cacheMutex.Unlock()

		return nil
	})
}

// GetDisk возвращает информацию о диске по ID
func (m *DiskManager) GetDisk(ctx context.Context, diskID string) (*model.Disk, error) {
	// Сначала проверяем кэш
	m.cacheMutex.RLock()
	cachedDisk, exists := m.diskCache[diskID]
	m.cacheMutex.RUnlock()

	if exists {
		return cachedDisk, nil
	}

	// Получаем диск через API Kubernetes
	diskObj, err := m.dynamicClient.Resource(VMDiskGVR).Namespace(m.namespace).Get(ctx, diskID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get disk: %v", err)
	}

	// Конвертируем в модель диска
	disk, err := m.convertToDiskModel(diskObj)
	if err != nil {
		return nil, err
	}

	// Сохраняем в кэш
	m.cacheMutex.Lock()
	m.diskCache[diskID] = disk
	m.cacheMutex.Unlock()

	return disk, nil
}

// ListDisks возвращает список всех дисков
func (m *DiskManager) ListDisks(ctx context.Context) ([]*model.Disk, error) {
	// Обновляем кэш
	err := m.refreshDiskCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh disk cache: %v", err)
	}

	// Возвращаем список дисков из кэша
	m.cacheMutex.RLock()
	defer m.cacheMutex.RUnlock()

	disks := make([]*model.Disk, 0, len(m.diskCache))
	for _, disk := range m.diskCache {
		disks = append(disks, disk)
	}

	return disks, nil
}

// ResizeDisk изменяет размер диска
func (m *DiskManager) ResizeDisk(ctx context.Context, diskID string, newSizeGB int) (*model.Disk, error) {
	// Получаем текущую информацию о диске
	currentDisk, err := m.GetDisk(ctx, diskID)
	if err != nil {
		return nil, err
	}

	// Проверяем, что новый размер больше текущего
	if int32(newSizeGB) <= currentDisk.SizeGb {
		return nil, fmt.Errorf("new size (%d GB) must be greater than current size (%d GB)", newSizeGB, currentDisk.SizeGb)
	}

	// Получаем текущий ресурс диска
	diskObj, err := m.dynamicClient.Resource(VMDiskGVR).Namespace(m.namespace).Get(ctx, diskID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get disk for resizing: %v", err)
	}

	// Обновляем спецификацию диска
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Получаем актуальную версию ресурса перед каждой попыткой
		diskObj, err := m.dynamicClient.Resource(VMDiskGVR).Namespace(m.namespace).Get(ctx, diskID, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// Обновляем размер диска
		spec, found, err := unstructured.NestedMap(diskObj.Object, "spec")
		if err != nil || !found {
			return fmt.Errorf("spec not found in disk object")
		}

		spec["storage"] = fmt.Sprintf("%dGi", newSizeGB)
		err = unstructured.SetNestedMap(diskObj.Object, spec, "spec")
		if err != nil {
			return err
		}

		// Применяем изменения
		_, err = m.dynamicClient.Resource(VMDiskGVR).Namespace(m.namespace).Update(ctx, diskObj, metav1.UpdateOptions{})
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to resize disk: %v", err)
	}

	// Обновляем кэш
	m.cacheMutex.Lock()
	if disk, exists := m.diskCache[diskID]; exists {
		disk.SizeGb = newSizeGB
	}
	m.cacheMutex.Unlock()

	// Возвращаем обновленную информацию о диске
	return m.GetDisk(ctx, diskID)
}

// waitForDiskReady ожидает, пока диск перейдет в состояние Ready
func (m *DiskManager) waitForDiskReady(ctx context.Context, diskID string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeout := time.After(10 * time.Minute)

	for {
		select {
		case <-timeout:
			// Превышен таймаут ожидания
			m.cacheMutex.Lock()
			if disk, exists := m.diskCache[diskID]; exists {
				disk.Status = "ERROR"
			}
			m.cacheMutex.Unlock()
			return
		case <-ticker.C:
			// Получаем обновленную информацию о диске
			diskObj, err := m.dynamicClient.Resource(VMDiskGVR).Namespace(m.namespace).Get(ctx, diskID, metav1.GetOptions{})
			if err != nil {
				// Возможно диск был удален
				continue
			}

			// Проверяем статус диска
			status, found, _ := unstructured.NestedMap(diskObj.Object, "status")
			if !found {
				continue
			}

			phase, found := status["phase"]
			if !found {
				continue
			}

			phaseStr := phase.(string)

			// Обновляем статус в кэше
			m.cacheMutex.Lock()
			if disk, exists := m.diskCache[diskID]; exists {
				if phaseStr == "Ready" {
					disk.Status = "ACTIVE"
				} else if phaseStr == "Failed" {
					disk.Status = "ERROR"
				} else {
					disk.Status = "CREATING"
				}
			}
			m.cacheMutex.Unlock()

			// Если диск готов или произошла ошибка, выходим из цикла
			if phaseStr == "Ready" || phaseStr == "Failed" {
				return
			}
		}
	}
}

// refreshDiskCache обновляет кэш дисков
func (m *DiskManager) refreshDiskCache(ctx context.Context) error {
	// Получаем список дисков через API Kubernetes
	diskList, err := m.dynamicClient.Resource(VMDiskGVR).Namespace(m.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list disks: %v", err)
	}

	// Создаем новый кэш
	newCache := make(map[string]*model.Disk)

	// Обрабатываем каждый диск
	for _, diskObj := range diskList.Items {
		disk, err := m.convertToDiskModel(&diskObj)
		if err != nil {
			// Логируем ошибку и продолжаем
			fmt.Printf("Warning: Failed to convert disk to model: %v\n", err)
			continue
		}

		newCache[disk.DiskID] = disk
	}

	// Обновляем кэш
	m.cacheMutex.Lock()
	m.diskCache = newCache
	m.cacheMutex.Unlock()

	return nil
}

// periodicCacheRefresh периодически обновляет кэш дисков
func (m *DiskManager) periodicCacheRefresh() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx := context.Background()
			err := m.refreshDiskCache(ctx)
			if err != nil {
				fmt.Printf("Warning: Failed to refresh disk cache: %v\n", err)
			}
		}
	}
}

// convertToDiskModel преобразует Kubernetes ресурс в модель диска
func (m *DiskManager) convertToDiskModel(diskObj *unstructured.Unstructured) (*model.Disk, error) {
	metadata := diskObj.Object["metadata"].(map[string]interface{})

	// Извлекаем спецификацию диска
	spec, found, err := unstructured.NestedMap(diskObj.Object, "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("spec not found in disk object")
	}

	// Извлекаем статус диска
	status, found, _ := unstructured.NestedMap(diskObj.Object, "status")
	if !found {
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
	phase, found := status["phase"]
	if found {
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
		SizeGb:   sizeGB,
		Bootable: true, // Предполагаем, что все диски загрузочные
		Status:   diskStatus,
	}

	return disk, nil
}

// initializeImageURLs инициализирует маппинг ID образов на URL
func initializeImageURLs() map[string]string {
	return map[string]string{
		"ubuntu-24-04-noble": "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		"ubuntu-22-04-jammy": "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
		"debian-12-bookworm": "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2",
		"centos-stream-9":    "https://cloud.centos.org/centos/9-stream/x86_64/images/CentOS-Stream-GenericCloud-9-latest.x86_64.qcow2",
		"almalinux-9":        "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2",
		"fedora-38":          "https://download.fedoraproject.org/pub/fedora/linux/releases/38/Cloud/x86_64/images/Fedora-Cloud-Base-38-1.6.x86_64.qcow2",
		"opensuse-leap-15-5": "https://download.opensuse.org/distribution/leap/15.5/appliances/openSUSE-Leap-15.5-JeOS.x86_64-15.5.0-OpenStack-Cloud.qcow2",
	}
}
