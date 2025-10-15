package assets

import (
	"embed"
	"encoding/base64"
	"log"
)

var (
	//go:embed images/*
	imagesFs embed.FS
	//go:embed js/main.js
	mainJS []byte
)

var (
	OAPLogoInverse string
	OvalGreen      string
	OvalOrange     string
	OvalRed        string
	OvalGray       string
	OvalCheck      string
	MainJS         string
)

func init() {
	OAPLogoInverse = getSvgStringForFile("images/OpenAudioProtocol-Logo-inverse-v1.0.svg")
	OvalGreen = getSvgStringForFile("images/OvalGreen.svg")
	OvalOrange = getSvgStringForFile("images/OvalOrange.svg")
	OvalRed = getSvgStringForFile("images/OvalRed.svg")
	OvalGray = getSvgStringForFile("images/OvalGray.svg")
	OvalCheck = getSvgStringForFile("images/OvalCheck.svg")
	MainJS = string(mainJS)
}

func getSvgStringForFile(file string) string {
	svgContent, err := imagesFs.ReadFile(file)
	if err != nil {
		log.Fatalf("SVG not found: %v", err)
	}
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(svgContent)
}
