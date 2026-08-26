#include <gtest/gtest.h>

#include <fcntl.h>
#include <sys/socket.h>
#include <unistd.h>

#include <cstring>
#include <string>

#include "network/connection.h"
#include "network/protocol.h"

using minikv::network::Connection;
using minikv::network::RequestHeader;
using minikv::network::kProtocolMagic;

namespace {

void setNonblock(int fd) {
    int flags = ::fcntl(fd, F_GETFL, 0);
    ::fcntl(fd, F_SETFL, flags | O_NONBLOCK);
}

}  // namespace

// M8: header that claims a huge body must close the connection without
// waiting for gigabytes of payload.
TEST(ConnectionBoundsTest, HugeDeclaredPayloadCloses) {
    int fds[2];
    ASSERT_EQ(::socketpair(AF_UNIX, SOCK_STREAM, 0, fds), 0);
    setNonblock(fds[0]);
    setNonblock(fds[1]);

    auto conn = std::make_shared<Connection>(
        fds[0],
        [](const std::string&) { return std::string("should-not-run"); });

    RequestHeader hdr{};
    hdr.magic = kProtocolMagic;
    hdr.cmd = 1;
    hdr.key_len = 1;
    hdr.val_len = 0x7FFFFFFFu;  // absurd

    ASSERT_EQ(::write(fds[1], &hdr, sizeof(hdr)),
              static_cast<ssize_t>(sizeof(hdr)));

    conn->onReadable();
    EXPECT_TRUE(conn->shouldClose());

    ::close(fds[1]);
    // Connection owns fds[0]
}

TEST(ConnectionBoundsTest, OversizedReadBufferCloses) {
    int fds[2];
    ASSERT_EQ(::socketpair(AF_UNIX, SOCK_STREAM, 0, fds), 0);
    setNonblock(fds[0]);
    setNonblock(fds[1]);

    auto conn = std::make_shared<Connection>(
        fds[0], [](const std::string&) { return std::string(); });

    // Feed more than kMaxRequestSize without a complete framed request.
    // Use a smaller local override via repeated appends — but kMax is 64MB
    // which is heavy for unit tests. Instead craft a header with
    // key_len+val_len just above the cap.
    RequestHeader hdr{};
    hdr.magic = kProtocolMagic;
    hdr.cmd = 1;
    hdr.key_len = static_cast<uint32_t>(Connection::kMaxRequestSize);
    hdr.val_len = 1;  // total > kMaxRequestSize

    ASSERT_EQ(::write(fds[1], &hdr, sizeof(hdr)),
              static_cast<ssize_t>(sizeof(hdr)));
    conn->onReadable();
    EXPECT_TRUE(conn->shouldClose());

    ::close(fds[1]);
}
