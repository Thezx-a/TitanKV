#include "skynet/config/config.h"
#include <fstream>
#include <iostream>
#include <memory>
#include <cctype>

namespace skynet {
namespace config {

namespace {

std::string trim(std::string s) {
    while (!s.empty() && (s.back() == '\r' || std::isspace(static_cast<unsigned char>(s.back())))) {
        s.pop_back();
    }
    size_t start = s.find_first_not_of(" \t");
    if (start == std::string::npos) return "";
    return s.substr(start);
}

}  // namespace

// Minimal YAML for TitanKV gateway.yaml:
// listen / health_check / limits key-values, and upstreams list items:
//   - host: 127.0.0.1
//     port: 18080
//     weight: 1
std::unique_ptr<Config> Config::load(const std::string& path) {
    auto cfg = std::make_unique<Config>();
    std::ifstream f(path);
    if (!f.is_open()) {
        std::cerr << "Cannot open config: " << path << std::endl;
        return nullptr;
    }

    std::string line;
    std::string section;
    UpstreamConfig* current_up = nullptr;

    while (std::getline(f, line)) {
        line = trim(line);
        if (line.empty() || line[0] == '#') continue;

        // Section header: "listen:" / "upstreams:" / ...
        if (!line.empty() && line.back() == ':' && line.find(':') == line.size() - 1) {
            section = line.substr(0, line.size() - 1);
            current_up = nullptr;
            continue;
        }

        // New upstream list item: "- host: 127.0.0.1" or bare "-"
        if (line.size() >= 1 && line[0] == '-') {
            cfg->upstreams.push_back(UpstreamConfig{});
            current_up = &cfg->upstreams.back();
            current_up->weight = 1;
            section = "upstreams";

            std::string rest = trim(line.substr(1));
            if (rest.empty()) continue;
            size_t colon = rest.find(':');
            if (colon == std::string::npos) continue;
            std::string key = trim(rest.substr(0, colon));
            std::string val = trim(rest.substr(colon + 1));
            if (key == "host") current_up->host = val;
            else if (key == "port") current_up->port = std::stoi(val);
            else if (key == "weight") current_up->weight = std::stoi(val);
            continue;
        }

        size_t colon = line.find(':');
        if (colon == std::string::npos) continue;
        std::string key = trim(line.substr(0, colon));
        std::string val = trim(line.substr(colon + 1));

        if (section == "listen") {
            if (key == "port") cfg->listen_port = std::stoi(val);
            else if (key == "threads") cfg->worker_threads = std::stoi(val);
        } else if (section == "health_check") {
            if (key == "interval") cfg->health_check.interval_s = std::stoi(val);
            else if (key == "timeout") cfg->health_check.timeout_ms = std::stoi(val);
            else if (key == "path") cfg->health_check.path = val;
        } else if (section == "limits") {
            if (key == "max_connections") cfg->limits.max_connections = std::stoi(val);
            else if (key == "per_ip_max") cfg->limits.per_ip_max = std::stoi(val);
        } else if (section == "upstreams" && current_up) {
            if (key == "host") current_up->host = val;
            else if (key == "port") current_up->port = std::stoi(val);
            else if (key == "weight") current_up->weight = std::stoi(val);
        }
    }

    if (cfg->upstreams.empty()) {
        std::cerr << "Config warning: no upstreams parsed from " << path << std::endl;
    } else {
        std::cerr << "Config loaded " << cfg->upstreams.size() << " upstream(s): ";
        for (const auto& u : cfg->upstreams) {
            std::cerr << u.host << ":" << u.port << "(w=" << u.weight << ") ";
        }
        std::cerr << std::endl;
    }
    return cfg;
}

}  // namespace config
}  // namespace skynet
