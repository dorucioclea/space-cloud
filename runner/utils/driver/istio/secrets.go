package istio

import (
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	kubeErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/spaceuptech/space-cloud/runner/model"
)

// CreateSecret is used to upsert secret
func (i *Istio) CreateSecret(projectID string, secretObj *model.Secret) error {
	// check whether the oldSecret type is correct!
	if secretObj.Type != model.FileType && secretObj.Type != model.EnvType && secretObj.Type != model.DockerType {
		return fmt.Errorf("invalid oldSecret type (%s) provided", secretObj.Type)
	}

	oldSecret, err := i.kube.CoreV1().Secrets(projectID).Get(secretObj.Name, metav1.GetOptions{})
	if kubeErrors.IsNotFound(err) {
		// Create a new Secret
		logrus.Debugf("Creating oldSecret (%s)", secretObj.Name)
		newSecret, err := generateSecret(projectID, secretObj)
		if err != nil {
			return err
		}

		_, err = i.kube.CoreV1().Secrets(projectID).Create(newSecret)
		return err

	} else if err == nil {
		// oldSecret already exists...update it!
		logrus.Debugf("Updating oldSecret (%s)", secretObj.Name)
		if string(oldSecret.Type) != secretObj.Type {
			return fmt.Errorf("secret already exists type mismatch")
		}
		newSecret, err := generateSecret(projectID, secretObj)
		if err != nil {
			return err
		}
		_, err = i.kube.CoreV1().Secrets(projectID).Update(newSecret)
		return err
	}
	logrus.Errorf("Failed to create oldSecret (%s) - %s", secretObj.Name, err)
	return err
}

// ListSecrets lists all the secrets in the provided name-space!
func (i *Istio) ListSecrets(projectID string) ([]*model.Secret, error) {
	// List all secrets
	kubeSecret, err := i.kube.CoreV1().Secrets(projectID).List(metav1.ListOptions{LabelSelector: "app=space-cloud"})
	if err != nil {
		logrus.Errorf("Failed to fetch list of secrets - %s", err)
		return nil, err
	}
	listOfSecrets := make([]*model.Secret, len(kubeSecret.Items))

	// Modifying SecretValue with empty []byte
	for i, v := range kubeSecret.Items {
		s := &model.Secret{
			Name:     v.ObjectMeta.Name,
			Type:     v.ObjectMeta.Annotations["secretType"],
			RootPath: v.ObjectMeta.Annotations["rootPath"],
			Data:     make(map[string]string, len(v.Data)),
		}
		if s.Type == model.FileType || s.Type == model.EnvType {
			for k1 := range v.Data {
				s.Data[k1] = ""
			}
		} else if s.Type == model.DockerType {
			s.Data["username"] = ""
			s.Data["password"] = ""
			s.Data["url"] = ""
		}
		listOfSecrets[i] = s
	}
	return listOfSecrets, nil
}

// DeleteSecret is used to delete secrets!
func (i *Istio) DeleteSecret(projectID string, secretName string) error {
	err := i.kube.CoreV1().Secrets(projectID).Delete(secretName, &metav1.DeleteOptions{})
	if kubeErrors.IsNotFound(err) || err == nil {
		return nil
	}
	logrus.Errorf("Failed to delete secret (%s) - %s", secretName, err)
	return err
}

func (i *Istio) SetFileSecretRootPath(projectId string, secretName, rootPath string) error {
	if secretName == "" || rootPath == "" {
		logrus.Errorf("empty secret name or root path provided")
		return fmt.Errorf("empty secret name or root path provided got (%s,%s)", secretName, rootPath)
	}
	// Get secret and then check type
	kubeSecret, err := i.kube.CoreV1().Secrets(projectId).Get(secretName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// update root path
	switch kubeSecret.Type {
	case v1.SecretTypeDockerConfigJson:
		return fmt.Errorf("set root path operation cannot be performed on secrets with type docker")
	case v1.SecretTypeOpaque:
		kubeSecret.Annotations["rootPath"] = rootPath
	default:
		return fmt.Errorf("invalid secret type - %s", kubeSecret.Type)
	}

	// Update the secret
	_, err = i.kube.CoreV1().Secrets(projectId).Update(kubeSecret)
	return err
}

// SetKey adds a new secret key-value pair
func (i *Istio) SetKey(projectID string, secretName string, secretKey string, secretValObj *model.SecretValue) error {
	if secretName == "" || secretValObj.Value == "" {
		logrus.Errorf("Empty key/value provided. Key not set")
		return fmt.Errorf("key/value not provided; got (%s,%s)", secretName, secretValObj.Value)
	}

	// Get secret and then check type
	kubeSecret, err := i.kube.CoreV1().Secrets(projectID).Get(secretName, metav1.GetOptions{})

	if kubeErrors.IsNotFound(err) {
		return err
	} else if err == nil {
		// Add secret key-value
		switch kubeSecret.Type {
		case v1.SecretTypeDockerConfigJson:
			return fmt.Errorf("set key operation cannot be performed on secrets with type docker")
		case v1.SecretTypeOpaque:
			kubeSecret.Data[secretKey] = []byte(secretValObj.Value)
		default:
			return fmt.Errorf("invalid secret type - %s", kubeSecret.Type)
		}

		// Update the secret
		_, err := i.kube.CoreV1().Secrets(projectID).Update(kubeSecret)
		return err
	}
	return err
}

// DeleteKey is used to delete a key from the secret!
func (i *Istio) DeleteKey(projectID string, secretName string, secretKey string) error {
	// Get secret
	kubeSecret, err := i.kube.CoreV1().Secrets(projectID).Get(secretName, metav1.GetOptions{})

	if kubeErrors.IsNotFound(err) {
		return fmt.Errorf("secret with name (%s) does not exist- %s", secretName, err)
	} else if err == nil {
		// Check the type of secret (docker/opaque)
		switch kubeSecret.Type {
		case v1.SecretTypeDockerConfigJson:
			return fmt.Errorf("delete key operation cannot be performed on secrets with type docker")
		case v1.SecretTypeOpaque:
			delete(kubeSecret.Data, secretKey)
		default:
			return fmt.Errorf("invalid secret type - %s", kubeSecret.Type)
		}

		// Update the secret
		_, err := i.kube.CoreV1().Secrets(projectID).Update(kubeSecret)
		return err
	}
	return err
}

// helper function
func generateSecret(projectID string, secret *model.Secret) (*v1.Secret, error) {
	encodedData := map[string][]byte{}
	var typeOfSecret v1.SecretType

	// Check what type of secret is to be created: file/env/docker
	switch secret.Type {
	case model.FileType, model.EnvType:
		typeOfSecret = v1.SecretTypeOpaque
		for k, v := range secret.Data {
			encodedData[k] = []byte(v)
		}
	case model.DockerType:
		username, p1 := secret.Data["username"]
		password, p2 := secret.Data["password"]
		url, p3 := secret.Data["url"]

		if !p1 || !p2 || !p3 {
			return nil, errors.New("incorrect secret value provided for secret type docker")
		}

		typeOfSecret = v1.SecretTypeDockerConfigJson
		authSecret := username + ":" + password
		encAuthSecret := b64.StdEncoding.EncodeToString([]byte(authSecret))
		// ref: https://kubernetes.io/docs/tasks/configure-pod-container/pull-image-private-registry/#registry-secret-existing-credentials
		dockerJSON := map[string]interface{}{
			"auths": map[string]interface{}{
				url: map[string]string{
					"auth": encAuthSecret,
				},
			},
		}
		data, _ := json.Marshal(dockerJSON)
		encodedData[v1.DockerConfigJsonKey] = []byte(data)
	default:
		return nil, fmt.Errorf("invalid secret type (%s) provided", secret.Type)
	}
	return &v1.Secret{
		Type: typeOfSecret,
		ObjectMeta: metav1.ObjectMeta{
			Name:        secret.Name,
			Namespace:   projectID,
			Labels:      map[string]string{"app": "space-cloud"},
			Annotations: map[string]string{"rootPath": secret.RootPath, "secretType": secret.Type},
		},
		Data: encodedData,
	}, nil
}
