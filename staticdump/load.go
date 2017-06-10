package staticdump

import (
	"archive/zip"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/evepraisal/go-evepraisal/typedb"
)

var userAgent = "go-evepraisal"

func MustFindLastStaticDumpURL() string {
	url := FindLastStaticDumpURL()
	if url == "" {
		log.Fatalf("Could not find static dump URL")
	}
	return url
}

func FindLastStaticDumpURL() string {
	i := 0
	current := time.Now()
	for i < 200 {
		url := "https://cdn1.eveonline.com/data/sde/tranquility/sde-" + current.Format("20060102") + "-TRANQUILITY.zip"
		req, err := http.NewRequest("HEAD", url, nil)
		if err != nil {
			log.Println("WARN: Unexpected building request: %s", err)
		}
		req.Header.Add("User-Agent", userAgent)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Println("WARN: Unexpected error during request: %s", err)
		}

		switch resp.StatusCode {
		case 200:
			return url
		case 404:
			current = current.Add(-24 * time.Hour)
			continue
		default:
			log.Println("Unexpected response when trying to find last static dump: %s", resp.Status)
		}
	}
	return ""
}

func LoadTypes(cachepath string, staticDumpURL string) ([]typedb.EveType, error) {
	if _, err := os.Stat(cachepath); os.IsNotExist(err) {
		log.Printf("Downloading static dump to %s", cachepath)
		err := download(staticDumpURL, cachepath)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	err := download(staticDumpURL, cachepath)
	if err != nil {
		return nil, err
	}

	return loadtypes(cachepath)
}

func download(staticDumpURL string, staticDataPath string) error {
	out, err := os.Create(staticDataPath)
	defer out.Close()
	if err != nil {
		return err
	}

	req, err := http.NewRequest("GET", staticDumpURL, nil)
	if err != nil {
		return err
	}

	req.Header.Add("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	log.Printf("Successfully wrote %d bytes to %s", n, staticDataPath)
	return nil
}

type Type struct {
	Name struct {
		En string
	}
	Published bool
	Volume    float64
	BasePrice float64
}

type Blueprint struct {
	BlueprintTypeID int64 `yaml:"blueprintTypeID"`
	Activities      struct {
		Manufacturing struct {
			Materials []struct {
				Quantity int64
				TypeID   int64 `yaml:"typeID"`
			}
			Products []struct {
				Quantity int64
				TypeID   int64 `yaml:"typeID"`
			}
		}
	}
}

func loadtypes(staticDataPath string) ([]typedb.EveType, error) {
	r, err := zip.OpenReader(staticDataPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var allTypes map[int64]Type
	err = loadDataFromZipFile(r, "sde/fsd/typeIDs.yaml", &allTypes)
	if err != nil {
		return nil, err
	}
	log.Printf("Loaded %d types", len(allTypes))

	var allBlueprints map[int64]Blueprint
	err = loadDataFromZipFile(r, "sde/fsd/blueprints.yaml", &allBlueprints)
	if err != nil {
		return nil, err
	}
	log.Printf("Loaded %d blueprints", len(allBlueprints))

	blueprintsByProductType := make(map[int64][]Blueprint)
	for _, blueprint := range allBlueprints {
		for _, product := range blueprint.Activities.Manufacturing.Products {
			blueprints, ok := blueprintsByProductType[product.TypeID]
			if ok {
				blueprintsByProductType[product.TypeID] = append(blueprints, blueprint)
			} else {
				blueprintsByProductType[product.TypeID] = []Blueprint{blueprint}
			}
		}
	}

	types := make([]typedb.EveType, 0)
	for typeID, t := range allTypes {
		if !t.Published {
			continue
		}

		eveType := typedb.EveType{
			ID:             typeID,
			Name:           t.Name.En,
			Volume:         t.Volume,
			BasePrice:      t.BasePrice,
			BaseComponents: flattenComponents(resolveBaseComponents(blueprintsByProductType, typeID, 1, 5)),
		}
		types = append(types, eveType)
	}

	return types, nil
}

func flattenComponents(components []typedb.Component) []typedb.Component {
	m := make(map[typedb.Component]int64)
	for _, component := range components {
		qty := component.Quantity
		component.Quantity = 0
		m[component] += qty
	}

	s := make([]typedb.Component, 0, len(m))
	for component, qty := range m {
		component.Quantity = qty
		s = append(s, component)
	}
	return s
}

func resolveBaseComponents(blueprintsByProductType map[int64][]Blueprint, typeID int64, multiplier int64, left int) []typedb.Component {
	if left == 0 {
		return nil
	}

	blueprints, ok := blueprintsByProductType[typeID]
	if !ok || len(blueprints) == 0 {
		return nil
	}

	bp := blueprints[0]
	var components []typedb.Component
	for _, material := range bp.Activities.Manufacturing.Materials {
		r := resolveBaseComponents(blueprintsByProductType, material.TypeID, material.Quantity*multiplier, left-1)
		if r == nil {
			components = append(components, typedb.Component{Quantity: material.Quantity * multiplier, TypeID: material.TypeID})
		} else {
			components = append(components, r...)
		}
	}
	return components
}
