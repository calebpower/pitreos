package main

import (
	"crypto/sha1"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"os"
	"sync"

	"cloud.google.com/go/storage"
	"github.com/abourget/llerrgroup"

	yaml "gopkg.in/yaml.v2"
)

var (
	StorageBucketName = "eoscanada-playground-pitr"
	ChunkSize         = uint64(250 * (1 << 20))
	StorageBucket     *storage.BucketHandle
	mutex             = &sync.Mutex{}
)

func isEmptyChunk(s []byte) bool {
	for _, v := range s {
		if v != 0 {
			return false
		}
	}
	return true
}

func downloadFileFromChunks(fm Filemeta) {
	fmt.Printf("%+v\n", fm)

	f, err := os.OpenFile(fm.FileName, os.O_RDWR, 0644)

	if err = f.Truncate(fm.TotalSize); err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err != nil {
		log.Fatal(err)
	}

	eg := llerrgroup.New(60)
	for _, i := range fm.Chunks {
		mutex.Lock()
		f.Seek(int64(i.Start), 0)
		partBuffer := make([]byte, int64(i.End-i.Start+1))
		n, err := f.Read(partBuffer)
		mutex.Unlock()
		if err != nil {
			log.Fatalf("error wtf: %s\n", err)
		}
		fmt.Printf("read that many bytes: %i\n", n)

		if isEmptyChunk(partBuffer) {
			fmt.Printf("block starting at %i is empty", i.Start)
			if i.IsEmpty {
				fmt.Println(".... Perfect, we are happy")
			} else {
				fmt.Printf("uh oh... it should be sha1sum of %s\n", i.Content)
				blobPath := fmt.Sprintf("%s.blob", i.Content)
				blobStart := int64(i.Start)
				eg.Go(func() error {
					newData, err := readFromGoogleStorage(blobPath)
					if err != nil {
						fmt.Printf("something went wrong reading, error = %s\n", err)
						return err
					}
					newSHA1Sum := sha1.Sum(newData)
					fmt.Printf("THIS IS THE NEW SUM %x\n", newSHA1Sum)
					mutex.Lock()
					f.Seek(blobStart, 0)
					f.Write(newData)
					mutex.Unlock()
					return nil
				})
			}
		} else {
			readSHA1Sum := sha1.Sum(partBuffer)
			shasum := fmt.Sprintf("%x", readSHA1Sum)
			fmt.Printf("we have this sha1: %s...", shasum)
			if shasum == i.Content {
				fmt.Println("we are so happy !!!! ")
			} else {
				fmt.Printf("uh oh .... we are expecting: %s\n", i.Content)
			}
		}

	}
	if err := eg.Wait(); err != nil {
		// eg.Wait() will block until everything is done, and return the first error.
		os.Exit(1)
	}
}

func uploadFileToChunks(fileName string, chunkSize uint64) {

	file, err := os.Open(fileName)

	if err != nil {
		log.Fatal(err)
	}

	var fm Filemeta
	fm.FileName = fileName

	defer file.Close()

	fileInfo, _ := file.Stat()

	var fileSize int64 = fileInfo.Size()
	fmt.Printf("filesize is %s\n", fileSize)
	fm.TotalSize = fileSize
	fm.BlobsLocation = "/here"

	// calculate total number of parts the file will be chunked into

	totalPartsNum := uint64(math.Ceil(float64(fileSize) / float64(chunkSize)))

	log.Printf("Splitting to %d pieces.\n", totalPartsNum)

	eg := llerrgroup.New(60)
	for i := uint64(0); i < totalPartsNum; i++ {

		fmt.Printf("### Processing part %d of %d ###\n", i, totalPartsNum)
		partSize := uint64(math.Min(float64(ChunkSize), float64(fileSize-int64(i*ChunkSize))))
		var cm Chunkmeta
		cm.Start = (i * ChunkSize)
		cm.End = cm.Start + partSize - 1
		partBuffer := make([]byte, partSize)

		fmt.Println("reading file...")
		file.Read(partBuffer)
		fmt.Println("done reading file")

		if isEmptyChunk(partBuffer) {
			fmt.Println("this part is empty")
			cm.IsEmpty = true
		} else {
			cm.Content = fmt.Sprintf("%x", sha1.Sum(partBuffer))
			fileName := cm.Content + ".blob"

			if eg.Stop() {
				continue // short-circuit the loop if we got an error
			}
			fmt.Println("will try to check a file in a go func")
			eg.Go(func() error {
				if checkFileExistsOnGoogleStorage(fileName) {
					log.Printf("File already exists: %s\n", fileName)
					return nil
				} else {
					log.Printf("Updating the file: %s\n", fileName)
					url, err := writeToGoogleStorage(fileName, partBuffer)
					fmt.Printf("Wrote file: %s\n", url)
					return err
				}

			})

		}

		fm.Chunks = append(fm.Chunks, cm)
	}
	if err := eg.Wait(); err != nil {
		// eg.Wait() will block until everything is done, and return the first error.
		os.Exit(1)
	}
	d, err := yaml.Marshal(&fm)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	fmt.Printf("file meta is: \n---\n%s\n", string(d))

}

func main() {

	// parse args
	command := "backup"
	fileName := "file.img"
	if len(os.Args) > 2 {
		fileName = os.Args[2]
	}
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	// initialize google storage
	var err error
	StorageBucket, err = configureStorage(StorageBucketName)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(command)
	// backup file
	if command == "backup" {
		uploadFileToChunks(fileName, ChunkSize)
		os.Exit(0)
	}

	if command == "restore" {
		var fm Filemeta

		y, err := ioutil.ReadFile(fileName)
		if err != nil {
			log.Fatal(err)
		}

		err = yaml.Unmarshal(y, &fm)
		if err != nil {
			log.Fatalf("error: %v", err)
		}
		downloadFileFromChunks(fm)
		os.Exit(0)
	}

}