# 模拟后续需求变更

Source-ID: SRC-CHANGE-01
Kind: synthetic_change_request
Authority: proposal_pending_confirmation
Previous-Decision: DEC-FIXTURE-01

模拟聊天补充：现在希望第一期也能跨文件重命名，并一键应用到项目里；还想支持 Java 8、11、17、21 的现有工程。

这是一条后续提议，不是对原确认记录的有效替换。分析应指出：与原只读决定冲突；涉及文件写入、引用更新、多模块边界、预览/确认、外部改动、部分失败恢复，以及项目JDK与JDT LS宿主JDK的区分。先列来源与影响，只提出一个当前可回答的关键问题，不把需求变化自动当成产品批准。
