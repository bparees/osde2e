package healthchecks

import (
	"context"
	"fmt"
	"log"

	"github.com/openshift/osde2e/pkg/common/logging"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/kubernetes/typed/batch/v1"
)

// CheckHealthcheckJob uses the `osd-cluster-ready` healthcheck job to determine cluster readiness. If the cluster
// is not ready, it will return an error.
func CheckHealthcheckJob(k8sClient *kubernetes.Clientset, ctx context.Context, logger *log.Logger) error {
	logger = logging.CreateNewStdLoggerOrUseExistingLogger(logger)

	logger.Print("Checking whether cluster is healthy before proceeding...")

	bv1C := k8sClient.BatchV1()
	namespace := "openshift-monitoring"
	name := "osd-cluster-ready"

	for {
		err := watchJob(bv1C, ctx, namespace, name)
		if err == nil {
			logger.Println("Healthcheck job passed")
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out while retrying from error: %w", err)
		default:
			logger.Printf("healthcheck failed, retrying for error: %v", err)
		}
	}
}

// watchJob establishes a watch on the provided job in the provided interface. It will return any errors
// it experiences while trying to establish this watch. If the watch succeeds, it will return nil only if
// the watched job succeeds. If the watched job fails, is disconnected, the watch produces an error, the
// watch channel closes, or the context is cancelled, it will return an error.
func watchJob(bv1C v1.BatchV1Interface, ctx context.Context, namespace, jobname string) error {
	jobs, err := bv1C.Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed listing jobs and timed out: %w", err)
	}
	for _, job := range jobs.Items {
		if job.Name != jobname {
			continue
		}
		if job.Status.Succeeded > 0 {
			return nil
		}
	}
	watcher, err := bv1C.Jobs(namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: jobs.ResourceVersion,
		FieldSelector:   "metadata.name=" + jobname,
	})
	if err != nil {
		return fmt.Errorf("failed watching job: %w", err)
	}
	for {
		select {
		case event, more := <-watcher.ResultChan():
			switch event.Type {
			case watch.Added:
				fallthrough
			case watch.Modified:
				job := event.Object.(*batchv1.Job)
				if job.Status.Succeeded > 0 {
					return nil
				}
				if job.Status.Failed > 0 {
					return fmt.Errorf("cluster readiness job failed")
				}
			case watch.Deleted:
				return fmt.Errorf("cluster readiness job deleted before becoming ready (this should never happen)")
			case watch.Error:
				return fmt.Errorf("watch returned error event: %v", event)
			}
			if !more {
				return fmt.Errorf("cluster watch result channel closed prematurely with event: %T %v", event, event)
			}
		case <-ctx.Done():
			return fmt.Errorf("healtcheck watch context cancelled while still waiting for success")
		}
	}
}
