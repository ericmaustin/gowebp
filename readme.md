# gowebp

gowebp is a tiny cli tool used to create webp images from jpegs and png files
the tool will create webp images alongside the jpeg and png files it finds in a target
directory with the same base file name

- requires libwebp to be installed

Usage:
```
Usage:
  -d string
        the directory to crawl
  -q uint
        the quality for the webp images
  -r    replace existing webp files
  -w int
        the number of worker routines to spawn. Defaults to number of CPUs. (default 16)
```

