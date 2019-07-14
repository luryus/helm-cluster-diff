package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	difflib "github.com/aryann/difflib"
	funk "github.com/thoas/go-funk"
	yaml "gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	discovery "k8s.io/client-go/discovery"
	dynamic "k8s.io/client-go/dynamic"
	rest "k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	helm "k8s.io/helm/pkg/helm"
	helm_release "k8s.io/helm/pkg/proto/hapi/release"
)

type parsedManifest struct {
	apiVersion string
	kind       string
	name       string
	manifest   map[string]interface{}
}

const ansiControlSequenceStart = "\x1B["
const ansiReset = ansiControlSequenceStart + "0m"
const ansiColorRed = ansiControlSequenceStart + "91m"
const ansiColorGreen = ansiControlSequenceStart + "92m"

func getReleaseNameArgument() string {
	if len(os.Args) != 2 {
		fmt.Printf("Usage: %s <release>\n", os.Args[0])
		os.Exit(1)
	}

	return os.Args[1]
}

func getRelease(releaseName string) (*helm_release.Release, error) {
	tillerHost, found := os.LookupEnv("TILLER_HOST")
	if !found {
		return nil, errors.New("TILLER_HOST not set")
	}

	hostOption := helm.Host(tillerHost)
	client := helm.NewClient(hostOption)

	res, err := client.ReleaseContent(releaseName)
	if err != nil {
		return nil, err
	}

	return res.Release, nil
}

func readKubeConfig() (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	return kubeConfig.ClientConfig()
}

func getDynamicKubeClient() (dynamic.Interface, error) {
	config, err := readKubeConfig()

	if err != nil {
		return nil, err
	}

	return dynamic.NewForConfig(config)
}

func getDiscoveryClient() (*discovery.DiscoveryClient, error) {
	config, err := readKubeConfig()

	if err != nil {
		return nil, err
	}

	return discovery.NewDiscoveryClientForConfig(config)
}

func getResource(
	discoveryClient *discovery.DiscoveryClient,
	dynamicClient dynamic.Interface,
	apiGroupVersionStr string,
	kind string, name string,
	namespace string) (*unstructured.Unstructured, error) {

	gv, err := schema.ParseGroupVersion(apiGroupVersionStr)
	if err != nil {
		return nil, fmt.Errorf("Parsing group version string %s failed: %v", apiGroupVersionStr, err)
	}

	res, err := discoveryClient.ServerResourcesForGroupVersion(apiGroupVersionStr)
	if err != nil {
		return nil, fmt.Errorf("Fetching server resources for %s failed: %v", apiGroupVersionStr, err)
	}

	var resourceType metav1.APIResource
	foundResourceType := false

	for _, resource := range res.APIResources {
		if resource.Kind == kind && !strings.ContainsRune(resource.Name, '/') {
			resourceType = resource
			foundResourceType = true
			break
		}
	}

	if !foundResourceType {
		return nil, fmt.Errorf("Did not find matching resource for %s %s", apiGroupVersionStr, kind)
	}

	gvr := schema.GroupVersionResource{
		Group:    gv.Group,
		Version:  gv.Version,
		Resource: resourceType.Name,
	}
	var resInterface dynamic.ResourceInterface
	if resourceType.Namespaced {
		resInterface = dynamicClient.Resource(gvr).Namespace(namespace)
	} else {
		resInterface = dynamicClient.Resource(gvr)
	}

	return resInterface.Get(name, metav1.GetOptions{})
}

func parseManifest(manifest string) ([]parsedManifest, error) {
	r := strings.NewReader(manifest)
	decoder := yaml.NewDecoder(r)

	parsed := make([]parsedManifest, 0)

	for {
		m := make(map[string]interface{})
		err := decoder.Decode(m)

		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		if !funk.Contains(m, "apiVersion") || !funk.Contains(m, "kind") || !funk.Contains(m, "metadata") || !funk.Contains(m["metadata"], "name") {
			log.Printf("Skipping %v\n", m)
			continue
		}

		parsed = append(parsed, parsedManifest{
			apiVersion: m["apiVersion"].(string),
			kind:       m["kind"].(string),
			name:       m["metadata"].(map[interface{}]interface{})["name"].(string),
			manifest:   m,
		})
	}

	return parsed, nil
}

func diffResources(releaseResource map[string]interface{}, clusterResource map[string]interface{}) {
	// remove things from the kube resource
	delete(clusterResource, "status")
	if funk.Contains(clusterResource, "metadata") {
		delete(clusterResource["metadata"].(map[string]interface{}), "creationTimestamp")
		delete(clusterResource["metadata"].(map[string]interface{}), "uid")
		delete(clusterResource["metadata"].(map[string]interface{}), "selfLink")
		delete(clusterResource["metadata"].(map[string]interface{}), "resourceVersion")
	}

	releaseBytes, err := yaml.Marshal(releaseResource)
	if err != nil {
		log.Fatal(err)
	}
	kubeBytes, err := yaml.Marshal(clusterResource)
	if err != nil {
		log.Fatal(err)
	}

	releaseLines := strings.Split(string(releaseBytes), "\n")
	kubeLines := strings.Split(string(kubeBytes), "\n")

	diff := difflib.Diff(releaseLines, kubeLines)
	for _, diffLine := range diff {
		l := diffLine.String()
		if strings.HasPrefix(l, "+") {
			fmt.Println(ansiColorGreen + l + ansiReset)
		} else if strings.HasPrefix(l, "-") {
			fmt.Println(ansiColorRed + l + ansiReset)
		} else {
			fmt.Println(diffLine)
		}
	}
}

func main() {
	release, err := getRelease(getReleaseNameArgument())
	if err != nil {
		log.Fatal("Getting Helm release failed: ", err)
	}

	mfst := release.GetManifest()
	releaseResources, err := parseManifest(mfst)
	if err != nil {
		log.Fatal("Parsing Helm release manifest failed: ", err)
	}

	dynamicClient, err := getDynamicKubeClient()
	if err != nil {
		log.Fatal("Creating Kubernetes dynamic client failed: ", err)
	}
	discoveryClient, err := getDiscoveryClient()
	if err != nil {
		log.Fatal("Creating Kubernetes discovery client failed: ", err)
	}

	clusterResources := make([]*unstructured.Unstructured, 0)
	for _, rr := range releaseResources {
		cr, err := getResource(
			discoveryClient, dynamicClient,
			rr.apiVersion, rr.kind, rr.name, release.Namespace)
		if err != nil {
			log.Fatal("Finding cluster resource ", rr.kind, " ", rr.name, " failed: ", err)
		}

		clusterResources = append(clusterResources, cr)
	}

	for i := range releaseResources {
		fmt.Println("===", releaseResources[i].kind, releaseResources[i].name, "===")
		diffResources(releaseResources[i].manifest, clusterResources[i].UnstructuredContent())
	}
}