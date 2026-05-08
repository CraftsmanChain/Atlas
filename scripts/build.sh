cd /Users/chuan/Documents/trae_projects/atlas/web
npm install
npm run build

cd /Users/chuan/Documents/trae_projects/atlas
bash scripts/build_linux_amd64.sh

rm -f dist.tar.gz
tar zcvf dist.tar.gz web/dist
osscp bin/linux-amd64/atlas-server
osscp dist.tar.gz

