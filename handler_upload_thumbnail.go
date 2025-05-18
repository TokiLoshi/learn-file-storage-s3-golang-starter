package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Implement the upload with 10 MB
	const maxMemory = 10 << 20  
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, 500, "error passing max memory", err)
		return
	}

	// use the image data from the form 
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

		// Read all the image data into a byte slice using io.ReadAll 
	mediaType := header.Header.Get("Content-Type")
	if mediaType == "" {
		respondWithError(w, http.StatusBadRequest, "Unable to get mediaType", nil)
		return
	}

	mediaContentType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Media type", nil)
		return
	}

	if mediaContentType != "image/jpeg" && mediaContentType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "Content is not an image", nil) 
		return
	}

	assetPath := getAssetPath(videoID, mediaType)
	assetDiskPath := cfg.getAssetDiskPath(assetPath)

	destinationFile, err := os.Create(assetDiskPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create file on server", err)
	}

	defer destinationFile.Close()

	if _, err = io.Copy(destinationFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error saving file", err)
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get video from db", err)
		return
	}

		// if the authenticated user is not the video owner return htttp.StatusUnauthorized response
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unathorized", fmt.Errorf("unauthorized"))	
		return
	}

	url := cfg.getAssetURL(assetPath)
	video.ThumbnailURL = &url
	
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video", err)
		return
	} 

	respondWithJSON(w, http.StatusOK, video)
}
