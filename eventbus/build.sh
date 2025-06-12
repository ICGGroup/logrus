#!/usr/bin/env bash

export IMAGE="872855011810.dkr.ecr.us-west-2.amazonaws.com/logevent:v0.1.28"

cd ui
npm run build
cd ..

docker build --provenance=false -t $IMAGE .
docker push $IMAGE